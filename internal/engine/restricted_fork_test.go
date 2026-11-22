package engine

import (
	"strings"
	"testing"
)

// A run for a pull request from a fork is restricted like an automation
// account's: it reads no secrets of the repository it targets (even when the
// project lets pull requests have them), gets no deploy and so no deploy lease,
// saves no cache, and its caches are those of a fork, which read only what the
// default branch saved. Approving it lifts the restriction for the steps not yet started.
func TestForkPullRequestRunReadsNoSecretsAndGetsNoDeploy(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, restrictedPipeline), Source: Source{Kind: "dir", Path: "/tmp"}, PullRequestSecrets: true,
		Restricted: &RestrictedEvent{Code: RestrictFork, Reason: "pull request from the fork ada/shop (ada)"},
		Trigger:    Trigger{Event: "pull_request", Ref: "refs/pull/3/head", PullRequest: 3, Author: "ada", Fork: true}})
	if err != nil {
		t.Fatal(err)
	}
	d, _ := e.GetRun(r.ID)
	if d.Restriction == nil || d.Restriction.Code != RestrictFork || d.Restriction.Approved {
		t.Fatalf("%+v", d.Restriction)
	}
	if sc := e.scopesOf(e.foldRun(t, r.ID)); sc.write != "" || sc.whyNone == "" {
		t.Fatalf("a fork's run saves a cache: %+v", sc)
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
			if s.SkipKind != SkipRestricted {
				t.Errorf("deploy: %q %q", s.SkipKind, s.Error)
			}
		}
	}
	// No deploy lease was taken for the environment.
	var leases int
	if err := e.db.QueryRow(`SELECT count(*) FROM deploy_locks WHERE run_id = ?`, r.ID).Scan(&leases); err != nil || leases != 0 {
		t.Errorf("a fork's run holds %d deploy locks (%v)", leases, err)
	}
}
