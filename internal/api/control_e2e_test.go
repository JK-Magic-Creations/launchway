package api

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

const controlPipeline = `pipeline: shop
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
    rollout: { name: shop, namespace: shop, pauses: wait }
`

func intRef(n int) *int { return &n }

// Commands over HTTP, with sign-in: the operator role must cover the
// deployment's environment, the person who started the run may not
// promote on a protected environment but may abort, a second promote is
// refused naming who asked, and everything is in the audit trail.
func TestCommandsOverHTTP(t *testing.T) {
	policy, err := auth.NewPolicy([]auth.Binding{
		{Group: "admins", Role: auth.Admin},
		{Group: "pay-ops", Role: auth.Operator, Projects: []string{"pay-*"}, Environments: []string{"prod"}},
		{User: "dana", Role: auth.Operator, Projects: []string{"pay-*"}, Environments: []string{"staging"}},
		{Group: "pay-view", Role: auth.Viewer, Projects: []string{"pay-*"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := setupAuth(t, envOpts{policy: policy,
		environments: []engine.Environment{{Name: "prod", Target: "kind", Projects: []string{"pay-*"}, Protected: true}},
		projects:     []source.Project{{Name: "pay-shop", URL: gitRepo(t, filepath.Join(dir, "shop"), controlPipeline), Poll: time.Hour}}})
	run, err := e.api.Projects.StartRun(context.Background(), "pay-shop", "main", "otto", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Build, apply, and a snapshot of the canary paused at step 1.
	ctx := context.Background()
	j, err := e.eng.Lease(ctx, engine.RunnerInfo{ID: "b", Name: "b"}, 0)
	if err != nil || j == nil {
		t.Fatalf("lease: %v", err)
	}
	e.eng.Start(j.LeaseID)
	zero := 0
	if err := e.eng.Finish(j.LeaseID, engine.Result{Outcome: "succeeded", ExitCode: &zero, Outputs: map[string]string{"tag": "2"}}); err != nil {
		t.Fatal(err)
	}
	dr := engine.RunnerInfo{ID: "d", Name: "deployer", Targets: []string{"kind"}}
	dj, err := e.eng.LeaseDeploy(ctx, dr, dr.Targets, 0)
	if err != nil || dj == nil {
		t.Fatalf("deploy lease: %v", err)
	}
	e.eng.Start(dj.LeaseID)
	if err := e.eng.Finish(dj.LeaseID, engine.Result{Outcome: "succeeded", Deploy: &engine.DeployFacts{Commit: "c2", Changed: true}}); err != nil {
		t.Fatal(err)
	}
	items, _, err := e.eng.Watch(ctx, dr, dr.Targets, "-", 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("watch: %+v %v", items, err)
	}
	yes := true
	snap := deploy.Snapshot{App: &deploy.App{Found: true, Sync: "Synced", Health: "Suspended", Contains: &yes},
		Rollout: &deploy.Rollout{Found: true, Phase: "Paused", ObservedKnown: true, CurrentPodHash: "h2", StableRS: "h1", Step: intRef(1), Steps: 3,
			Paused: true, PauseReasons: []string{"CanaryPauseStep"}, PauseIndefinite: true}}
	if _, err := e.eng.Observe(dr, dr.Targets, []engine.Observation{{Key: items[0].Key, Snapshot: snap}}); err != nil {
		t.Fatal(err)
	}
	d, _ := e.eng.GetRun(run.ID)
	dep := d.Deployments[0].ID
	base := e.srv.URL + "/api/v1/runs/" + run.ID + "/deployments/" + dep + "/"

	otto, ada, dana, vic := e.signIn(t, "otto"), e.signIn(t, "ada"), e.signIn(t, "dana"), e.signIn(t, "vic")
	if r := vic.do(http.MethodPost, base+"promote", ""); r.status != http.StatusForbidden {
		t.Fatalf("a viewer promoting: %d %s", r.status, r.body)
	}
	if r := dana.do(http.MethodPost, base+"promote", ""); r.status != http.StatusForbidden || !strings.Contains(string(r.body), "in environment prod") {
		t.Fatalf("an operator of staging only: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodPost, base+"promote", ""); r.status != http.StatusForbidden || !strings.Contains(string(r.body), "protected") {
		t.Fatalf("the starter promoting on a protected environment: %d %s", r.status, r.body)
	}
	var ctl struct {
		Protected bool             `json:"protected"`
		Controls  []engine.Control `json:"controls"`
	}
	otto.get(base + "controls").json(t, &ctl)
	if !ctl.Protected || ctl.Controls[0].Available || !strings.Contains(ctl.Controls[0].Reason, "protected") || ctl.Controls[2].Action != "abort" || !ctl.Controls[2].Available {
		t.Fatalf("otto's controls: %+v", ctl)
	}
	dana.get(base + "controls").json(t, &ctl)
	if ctl.Controls[2].Available || !strings.Contains(ctl.Controls[2].Reason, "environment prod") {
		t.Fatalf("dana's controls: %+v", ctl)
	}
	r := ada.do(http.MethodPost, base+"promote", `{"comment":"canary looks fine"}`)
	var cmd engine.CommandView
	r.json(t, &cmd)
	if r.status != http.StatusCreated || cmd.Actor != "ada" || cmd.Expect.PodHash != "h2" {
		t.Fatalf("ada promoting: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPost, base+"promote", ""); r.status != http.StatusConflict || !strings.Contains(string(r.body), "already asked for by ada") {
		t.Fatalf("promoting twice: %d %s", r.status, r.body)
	}
	if r := dana.do(http.MethodPost, base+"commands/"+cmd.ID+"/withdraw", ""); r.status != http.StatusForbidden {
		t.Fatalf("dana withdrawing: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodPost, base+"commands/"+cmd.ID+"/withdraw", ""); r.status != http.StatusOK {
		t.Fatalf("otto withdrawing: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodPost, base+"abort", `{"comment":"error rate"}`); r.status != http.StatusCreated {
		t.Fatalf("the starter aborting: %d %s", r.status, r.body)
	}
	log, err := e.eng.AuditLog(engine.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, a := range log {
		if strings.HasPrefix(a.Action, "deployment.") {
			seen[a.User+" "+a.Action] = a.Outcome
		}
	}
	for want, outcome := range map[string]string{"ada deployment.promote": "ok", "otto deployment.promote": "denied",
		"dana deployment.promote": "denied", "otto deployment.withdraw": "ok", "otto deployment.abort": "ok"} {
		if seen[want] != outcome {
			t.Errorf("audit %q: %q, want %q (all: %v)", want, seen[want], outcome, seen)
		}
	}
}
