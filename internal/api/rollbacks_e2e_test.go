package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

func postJSON(t *testing.T, url string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(csrfHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// A successful deployment is rolled back over HTTP with a real deploy
// runner, a real Git repository and a stand-in Argo CD: three versions are
// deployed, then the service is rolled back to the first; Git holds two
// revert commits (newest change first), the file shows the first version,
// and what the server reports before and after is what happened.
func TestRollbackAfterSuccessEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	de := setupDeploy(t)
	de.deployRunner(t, "deployer-1")
	base := de.srv.URL
	for _, v := range []string{"1.1.0", "1.2.0", "1.3.0"} {
		if r := waitDeployRun(t, de.eng, de.startShop(t, v, "true"), time.Minute); r.Status != engine.RunSucceeded {
			t.Fatalf("%s: %s %+v", v, r.Status, r.StepList)
		}
	}
	// What the server reports.
	var plan engine.RollbackPlan
	getJSON(t, base+"/api/v1/projects/shop/rollback?environment=prod", &plan)
	if !plan.Action.Available || len(plan.Action.Targets) != 2 || plan.Action.Targets[0].Version != "1.2.0" || plan.Current.Version != "1.3.0" {
		t.Fatalf("plan: %+v", plan)
	}
	// Refusals are plain.
	var e struct{ Error string }
	if code := postJSON(t, base+"/api/v1/projects/shop/rollbacks", map[string]any{"environment": "prod"}, &e); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest ||
		!strings.Contains(e.Error, "needs a reason") {
		t.Fatalf("no reason: %d %+v", code, e)
	}
	if code := postJSON(t, base+"/api/v1/projects/shop/rollbacks", map[string]any{"reason": "x"}, &e); code != http.StatusBadRequest {
		t.Fatalf("no environment: %d %+v", code, e)
	}
	// Roll back to 1.1.0 by its version.
	var sum engine.RunSummary
	if code := postJSON(t, base+"/api/v1/projects/shop/rollbacks", map[string]any{"environment": "prod", "to": "1.1.0", "reason": "checkout is down"}, &sum); code != http.StatusCreated {
		t.Fatalf("rollback: %d", code)
	}
	if sum.Event != "rollback" || sum.Actor != "tester" || !strings.Contains(sum.Message, "1.3.0 → 1.1.0, by tester: checkout is down") {
		t.Fatalf("summary: %+v", sum)
	}
	r := waitDeployRun(t, de.eng, sum.ID, time.Minute)
	if r.Status != engine.RunSucceeded || stepByID(r, "rollback").Status != engine.StepSucceeded {
		t.Fatalf("rollback run: %s %+v", r.Status, r.StepList)
	}
	if content := gitIn(t, de.dir, "--git-dir", de.bare, "show", "main:apps/shop.yaml"); !strings.Contains(content, "team/shop:1.1.0") {
		t.Fatalf("file after the rollback:\n%s", content)
	}
	log := gitIn(t, de.dir, "--git-dir", de.bare, "log", "-2", "--format=%s")
	if !strings.Contains(log, "revert 1.3.0") || !strings.Contains(log, "revert 1.2.0") {
		t.Fatalf("two reverts expected:\n%s", log)
	}
	if len(r.Deployments) != 1 || r.Deployments[0].Mode != "rollback" || r.Deployments[0].Version != "1.1.0" || r.Deployments[0].PreviousVersion != "1.3.0" ||
		r.Deployments[0].Rollback == nil || r.Deployments[0].Rollback.Reason != "checkout is down" {
		t.Fatalf("deployments: %+v", r.Deployments)
	}
	// Links both ways, and nothing earlier is offered now.
	var d engine.RunDetail
	runs, err := de.eng.ListRuns(engine.RunFilter{Project: "shop", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Number == 3 {
			getJSON(t, base+"/api/v1/runs/"+run.ID, &d)
		}
	}
	if len(d.RolledBackBy) != 1 || d.RolledBackBy[0].ID != sum.ID {
		t.Fatalf("the run that was undone: %+v", d.RolledBackBy)
	}
	getJSON(t, base+"/api/v1/projects/shop/rollback?environment=prod", &plan)
	if plan.Action.Available || !strings.Contains(plan.Action.Reason, "no earlier successful deployment") || plan.Current.Version != "1.1.0" {
		t.Fatalf("plan after: %+v", plan)
	}
	// Unavailable is refused with the same sentence.
	if code := postJSON(t, base+"/api/v1/projects/shop/rollbacks", map[string]any{"environment": "prod", "reason": "again"}, &e); code != http.StatusConflict || e.Error != plan.Action.Reason {
		t.Fatalf("refusal: %d %q vs %q", code, e.Error, plan.Action.Reason)
	}
	// Not offered for what is not configured.
	if code := postJSON(t, base+"/api/v1/projects/shop/rollbacks", map[string]any{"environment": "nowhere", "reason": "x"}, &e); code != http.StatusNotFound {
		t.Fatalf("unknown environment: %d %+v", code, e)
	}
}

const rollbackPipeline = `pipeline: shop
steps:
  build:
    image: none
    run: echo tag=2 >> "$LAUNCHWAY_OUTPUTS"
  deploy:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } }
`

// rollbackSetup is a server with sign-in and a protected environment prod,
// where pay-shop has been deployed twice (by otto), so the service can be
// rolled back to the first.
func rollbackSetup(t *testing.T) *authEnv {
	t.Helper()
	dir := t.TempDir()
	e := setupAuth(t, envOpts{
		environments: []engine.Environment{{Name: "prod", Target: "kind", Projects: []string{"pay-*"}, Protected: true}},
		projects:     []source.Project{{Name: "pay-shop", URL: gitRepo(t, filepath.Join(dir, "shop"), rollbackPipeline), Poll: time.Hour}}})
	ctx := context.Background()
	dr := engine.RunnerInfo{ID: "d", Name: "deployer", Targets: []string{"kind"}}
	yes := true
	for i, tag := range []string{"1.0", "2.0"} {
		run, err := e.api.Projects.StartRun(ctx, "pay-shop", "main", "otto", nil)
		if err != nil {
			t.Fatal(err)
		}
		j, err := e.eng.Lease(ctx, engine.RunnerInfo{ID: "b", Name: "b"}, 0)
		if err != nil || j == nil || j.RunID != run.ID {
			t.Fatalf("lease: %+v %v", j, err)
		}
		e.eng.Start(j.LeaseID)
		zero := 0
		if err := e.eng.Finish(j.LeaseID, engine.Result{Outcome: "succeeded", ExitCode: &zero, Outputs: map[string]string{"tag": tag}}); err != nil {
			t.Fatal(err)
		}
		dj, err := e.eng.LeaseDeploy(ctx, dr, dr.Targets, 0)
		if err != nil || dj == nil {
			t.Fatalf("deploy lease: %v", err)
		}
		e.eng.Start(dj.LeaseID)
		if err := e.eng.Finish(dj.LeaseID, engine.Result{Outcome: "succeeded", Deploy: &engine.DeployFacts{Repo: "gitops", Branch: "main", Commit: fmt.Sprintf("c%d", i), Changed: true}}); err != nil {
			t.Fatal(err)
		}
		items, _, err := e.eng.Watch(ctx, dr, dr.Targets, "-", 0)
		if err != nil || len(items) != 1 {
			t.Fatalf("watch: %+v %v", items, err)
		}
		snap := deploy.Snapshot{App: &deploy.App{Found: true, Sync: "Synced", Health: "Healthy", Contains: &yes, Automated: true}}
		if _, err := e.eng.Observe(dr, dr.Targets, []engine.Observation{{Key: items[0].Key, Snapshot: snap}}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// Who may roll back, and review: a viewer and an operator of another
// environment are refused and the server's action says so first; an
// operator rolls back a protected environment as an emergency, no approval
// first; the person who asked may not review it, an administrator may; and
// it is all in the audit trail.
func TestRollbackAccessOverHTTP(t *testing.T) {
	e := rollbackSetup(t)
	otto, ada, vic, dana := e.signIn(t, "otto"), e.signIn(t, "ada"), e.signIn(t, "vic"), e.signIn(t, "dana")
	opts := e.srv.URL + "/api/v1/projects/pay-shop/rollback?environment=prod"
	post := e.srv.URL + "/api/v1/projects/pay-shop/rollbacks"

	var plan engine.RollbackPlan
	otto.get(opts).json(t, &plan)
	if !plan.Action.Available || !plan.Action.Emergency || len(plan.Action.Targets) != 1 || plan.Action.Targets[0].Version != "1.0" || !plan.Protected {
		t.Fatalf("otto's plan: %+v", plan.Action)
	}
	plan = engine.RollbackPlan{}
	vic.get(opts).json(t, &plan)
	if plan.Action.Available || len(plan.Action.Targets) != 0 || !strings.Contains(plan.Action.Reason, "operator role on pay-shop") || plan.Action.WouldHelp == "" {
		t.Fatalf("vic's plan: %+v", plan.Action)
	}
	// The same words refuse the request.
	if r := vic.do(http.MethodPost, post, `{"environment":"prod","reason":"x"}`); r.status != http.StatusForbidden {
		t.Fatalf("a viewer rolling back: %d %s", r.status, r.body)
	}
	if r := dana.do(http.MethodPost, post, `{"environment":"prod","reason":"x"}`); r.status != http.StatusNotFound && r.status != http.StatusForbidden {
		t.Fatalf("an operator of another project: %d %s", r.status, r.body)
	}
	var sum engine.RunSummary
	r := otto.do(http.MethodPost, post, `{"environment":"prod","reason":"payments fail since 2.0"}`)
	r.json(t, &sum)
	if r.status != http.StatusCreated || sum.Actor != "otto" || !strings.HasPrefix(sum.Message, "Emergency rollback of prod: 2.0 → 1.0, by otto: payments fail since 2.0") {
		t.Fatalf("otto rolling back: %d %s", r.status, r.body)
	}
	// Run details carry the rollback and its flag.
	var d engine.RunDetail
	otto.get(e.srv.URL+"/api/v1/runs/"+sum.ID).json(t, &d)
	if d.Rollback == nil || !d.Rollback.Emergency || !d.Rollback.ReviewRequired || len(d.StepList) != 1 {
		t.Fatalf("run: %+v", d.Rollback)
	}
	// The review list: otto sees it but may not acknowledge; ada may.
	var list []engine.ReviewItem
	otto.get(e.srv.URL+"/api/v1/rollbacks/review").json(t, &list)
	if len(list) != 1 || list[0].Run != sum.ID || list[0].Acknowledge.Available || !strings.Contains(list[0].Acknowledge.Reason, "someone else must review") {
		t.Fatalf("otto's review list: %+v", list)
	}
	list = nil
	ada.get(e.srv.URL+"/api/v1/rollbacks/review").json(t, &list)
	if len(list) != 1 || !list[0].Acknowledge.Available {
		t.Fatalf("ada's review list: %+v", list)
	}
	review := e.srv.URL + "/api/v1/runs/" + sum.ID + "/review"
	if r := otto.do(http.MethodPost, review, `{"comment":"fine"}`); r.status != http.StatusForbidden || !strings.Contains(string(r.body), "someone else must review") {
		t.Fatalf("otto reviewing his own: %d %s", r.status, r.body)
	}
	if r := vic.do(http.MethodPost, review, `{"comment":"fine"}`); r.status != http.StatusForbidden {
		t.Fatalf("a viewer reviewing: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, review, `{}`); r.status != http.StatusBadRequest && r.status != http.StatusUnprocessableEntity {
		t.Fatalf("a review without a comment: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, review, `{"comment":"checked the incident; the revert is right"}`); r.status != http.StatusOK {
		t.Fatalf("ada reviewing: %d %s", r.status, r.body)
	}
	list = nil
	ada.get(e.srv.URL+"/api/v1/rollbacks/review").json(t, &list)
	if len(list) != 0 {
		t.Fatalf("an acknowledged rollback is still listed: %+v", list)
	}
	seen := map[string]string{}
	for _, a := range e.auditEntries(t) {
		if strings.HasPrefix(a.Action, "deployment.rollback") || a.Action == "rollback.review" {
			seen[a.User+" "+a.Action] = a.Outcome
		}
	}
	for want, outcome := range map[string]string{"otto deployment.rollback": "ok", "vic deployment.rollback": "denied", "ada rollback.review": "ok", "vic rollback.review": "denied"} {
		if seen[want] != outcome {
			t.Errorf("audit %q: %q, want %q (all: %v)", want, seen[want], outcome, seen)
		}
	}
}
