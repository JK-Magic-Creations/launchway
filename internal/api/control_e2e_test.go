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
// controlSetup is a server with sign-in, protected environment prod, and a
// run of pay-shop started by otto whose deployment waits at a pause. It
// returns the deployment's URL base.
func controlSetup(t *testing.T) (*authEnv, string) {
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
	return e, e.srv.URL + "/api/v1/runs/" + run.ID + "/deployments/" + dep + "/"
}

func TestCommandsOverHTTP(t *testing.T) {
	e, base := controlSetup(t)
	otto, ada, dana, vic := e.signIn(t, "otto"), e.signIn(t, "ada"), e.signIn(t, "dana"), e.signIn(t, "vic")
	if r := vic.do(http.MethodPost, base+"promote", ""); r.status != http.StatusForbidden {
		t.Fatalf("a viewer promoting: %d %s", r.status, r.body)
	}
	if r := dana.do(http.MethodPost, base+"promote", ""); r.status != http.StatusForbidden || !strings.Contains(string(r.body), "in Airfield prod") {
		t.Fatalf("an operator of staging only: %d %s", r.status, r.body)
	}
	if r := otto.do(http.MethodPost, base+"promote", ""); r.status != http.StatusForbidden || !strings.Contains(string(r.body), "protected") {
		t.Fatalf("the starter promoting on a protected environment: %d %s", r.status, r.body)
	}
	var ctl struct {
		Protected bool             `json:"protected"`
		Controls  []engine.Control `json:"controls"`
	}
	otto.get(base+"controls").json(t, &ctl)
	if !ctl.Protected || ctl.Controls[0].Available || !strings.Contains(ctl.Controls[0].Reason, "protected") || ctl.Controls[2].Action != "abort" || !ctl.Controls[2].Available {
		t.Fatalf("otto's controls: %+v", ctl)
	}
	dana.get(base+"controls").json(t, &ctl)
	if ctl.Controls[2].Available || !strings.Contains(ctl.Controls[2].Reason, "Airfield prod") {
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

// A limited token never grants more than its maker has on the
// deployment's environment, and the protected-environment rule follows the
// person who started the run, whatever they use.
func TestCommandsWithLimitedTokens(t *testing.T) {
	e, base := controlSetup(t)
	ada, otto, dana := e.signIn(t, "ada"), e.signIn(t, "otto"), e.signIn(t, "dana")
	cli := newBrowser(t)
	post := func(tok, action string) response {
		t.Helper()
		return cli.do(http.MethodPost, base+action, `{"comment":"from a token"}`, bearer(tok)...)
	}
	_, adaView, _ := ada.makeToken(e.srv.URL, `{"name":"view","role":"viewer"}`)
	_, adaWeb, _ := ada.makeToken(e.srv.URL, `{"name":"web only","role":"operator","projects":["web"]}`)
	_, danaOp, _ := dana.makeToken(e.srv.URL, `{"name":"dana","role":"operator","projects":["pay-*"]}`)
	_, ottoOp, _ := otto.makeToken(e.srv.URL, `{"name":"otto","role":"operator","projects":["pay-shop"]}`)
	_, adaOp, _ := ada.makeToken(e.srv.URL, `{"name":"deployer","role":"operator","projects":["pay-shop"]}`)
	for _, tc := range []struct {
		who, tok, action string
		want             int
		says             string
	}{
		{"ada's viewer token", adaView, "promote", http.StatusForbidden, ""},
		{"ada's token for web only", adaWeb, "promote", http.StatusNotFound, ""},
		{"dana's operator token (dana operates staging only)", danaOp, "abort", http.StatusForbidden, "in Airfield prod"},
		{"otto's token (otto started the run)", ottoOp, "promote", http.StatusForbidden, "protected"},
		{"ada's operator token for pay-shop", adaOp, "promote", http.StatusCreated, ""},
		{"otto's token aborting", ottoOp, "abort", http.StatusCreated, ""},
	} {
		r := post(tc.tok, tc.action)
		if r.status != tc.want || !strings.Contains(string(r.body), tc.says) {
			t.Errorf("%s, %s: %d %s", tc.who, tc.action, r.status, r.body)
		}
	}
	// The controls say so too.
	var ctl struct {
		Controls []engine.Control `json:"controls"`
	}
	cli.get(base+"controls", bearer(danaOp)...).json(t, &ctl)
	for _, c := range ctl.Controls {
		if c.Available || !strings.Contains(c.Reason, "Airfield prod") {
			t.Errorf("dana's token's controls: %+v", ctl.Controls)
		}
	}
}
