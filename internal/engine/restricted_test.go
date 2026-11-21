package engine

import (
	"strings"
	"testing"
)

const restrictedPipeline = `pipeline: shop
on: { pull_request: {} }
steps:
  build:
    image: x
    run: build
  publish:
    needs: build
    image: x
    run: publish
    secrets: { TOKEN: { store: vault, key: apps/shop, field: token } }
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    timeout: 10m
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } }
    rollout: { name: shop, namespace: shop }
`

func restrictedRun(t *testing.T, e *Engine, secretsOn bool) *RunSummary {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, restrictedPipeline), Source: Source{Kind: "dir", Path: "/tmp"}, PullRequestSecrets: secretsOn,
		Restricted: &RestrictedEvent{Code: RestrictAutomation, Reason: "pull request from an automation account (dependabot[bot])"},
		Trigger:    Trigger{Event: "pull_request", Ref: "refs/pull/3/head", PullRequest: 3, Author: "dependabot[bot]"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A restricted run reads no secrets even if the project lets pull requests
// have them, deploys nothing, saves no cache; it says so on the run, and a
// maintainer's approval lifts the restrictions from the steps not yet started.
func TestRestrictedRunHasNoSecretsAndNoDeploys(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	r := restrictedRun(t, e, true)
	d, _ := e.GetRun(r.ID)
	if d.Restriction == nil || d.Restriction.Approved || !strings.Contains(d.Restriction.Summary, "restricted: pull request from an automation account") {
		t.Fatalf("%+v", d.Restriction)
	}
	trust := map[string]bool{}
	for _, a := range d.Actions {
		trust[a.Name] = a.Available
	}
	if !trust["run_with_secrets"] {
		t.Fatalf("no run_with_secrets action: %+v", d.Actions)
	}
	if sc := e.scopesOf(e.foldRun(t, r.ID)); sc.write != "" || sc.whyNone == "" {
		t.Fatalf("a restricted run saves a cache: %+v", sc)
	}
	ok(t, e, take(t, e), map[string]string{"tag": "v1"})
	got := statuses(t, e, r.ID)
	want(t, got, "publish", "failed", "deploy-prod", "skipped")
	d, _ = e.GetRun(r.ID)
	for _, s := range d.StepList {
		switch s.ID {
		case "publish":
			if !strings.Contains(s.Reason+s.Error, "this run is restricted") {
				t.Errorf("publish: %q", s.Reason+s.Error)
			}
		case "deploy-prod":
			if s.SkipKind != SkipRestricted || !strings.Contains(s.Error, "restricted: this run is for a pull request from an automation account") {
				t.Errorf("deploy: %q %q", s.SkipKind, s.Error)
			}
		}
	}
}

func TestApprovedRestrictedRunMayReadSecrets(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	r := restrictedRun(t, e, false)
	if err := e.TrustRun(r.ID, "grace", "read the diff"); err != nil {
		t.Fatal(err)
	}
	if err := e.TrustRun(r.ID, "grace", ""); err == nil {
		t.Fatal("approving twice should be refused")
	}
	d, _ := e.GetRun(r.ID)
	if d.Restriction == nil || !d.Restriction.Approved || d.Restriction.By != "grace" {
		t.Fatalf("%+v", d.Restriction)
	}
	ok(t, e, take(t, e), map[string]string{"tag": "v1"})
	if got := statuses(t, e, r.ID); got["publish"] == "failed" {
		t.Fatalf("an approved run's secret step was refused: %v", got)
	}
}

func TestUnrestrictedRunCannotBeApproved(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: y}\n", Trigger{})
	if err := e.TrustRun(id, "grace", ""); err == nil {
		t.Fatal("a run that is not restricted cannot be approved")
	}
}

func (e *Engine) foldRun(t *testing.T, id string) *RunState {
	t.Helper()
	evs, _, err := loadEvents(e.db, id)
	if err != nil {
		t.Fatal(err)
	}
	return e.fold(evs)
}
