package source

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// A pull request the repository's host says is from a fork runs restricted,
// with the pipeline of the default branch and not the one it brings, whoever
// its author is and whatever pull_requests.untrusted says; a first
// contribution waits for approval and then runs.
func TestForkPullRequestRunsRestrictedAndWaitsForApproval(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo, UntrustedAllowed: true})
	pollOnce(t, m)
	verdict := ForkVerdict{Fork: true, Reason: "pull request from the fork ada/app (ada)", Wait: "its author has nothing merged here"}
	m.SetForkGate(func(_ context.Context, project, _ string, number int, _ string) ForkVerdict {
		if project != "app" || number != 8 {
			t.Errorf("asked about %s #%d", project, number)
		}
		return verdict
	})
	run(t, repo, "git", "checkout", "-q", "--detach", "main")
	commitAs(t, repo, "Ada", evilPipeline, "a fork's change")
	run(t, repo, "git", "update-ref", "refs/pull/8/head", "HEAD")
	run(t, repo, "git", "checkout", "-q", "main")

	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("a first contribution ran before it was approved: %d runs", len(runs))
	}
	pollOnce(t, m) // still waiting: it is looked at again
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("%d runs", len(runs))
	}
	verdict.Wait = "" // approved
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 {
		t.Fatalf("%d runs after approval", len(runs))
	}
	d, _ := eng.GetRun(runs[0].ID)
	if d.Restriction == nil || d.Restriction.Code != engine.RestrictFork || d.Restriction.Approved || !strings.Contains(d.Restriction.Reason, "ada/app") {
		t.Fatalf("%+v", d.Restriction)
	}
	for _, s := range d.StepList {
		if s.ID == "evil" {
			t.Fatalf("a fork's run used the pipeline it brought: %v", d.StepList)
		}
	}
}

// A pull request from a fork gets the same restriction whichever way it is known to be one: by the fork gate of a
// repository this server hosts, or by the flag an outside host's provider gives. Both take the default branch's
// pipeline (not the one the fork brings), and both ignore the setting that lets automation accounts run their own,
// because they go through one function (forkPipelineSHA).
func TestForkRestrictionIsTheSameForHostedForksAndOutsideHostForks(t *testing.T) {
	restrictionOf := func(t *testing.T, how string) (*engine.Restriction, []string) {
		t.Helper()
		dir, repo := prRepo(t, prPipeline)
		m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo, UntrustedAllowed: true})
		pollOnce(t, m)
		switch how {
		case "hosted":
			m.SetForkGate(func(context.Context, string, string, int, string) ForkVerdict {
				return ForkVerdict{Fork: true, Reason: "pull request from a fork"}
			})
		case "outside":
			m.SetOutsideSource(fakeOutside{pr: func(n int) (PullRequestInfo, bool, error) {
				return PullRequestInfo{Number: n, Title: "x", Author: "mallory", Fork: true}, true, nil
			}})
		}
		run(t, repo, "git", "checkout", "-q", "--detach", "main")
		commitAs(t, repo, "Mallory", evilPipeline, "a fork's change")
		run(t, repo, "git", "update-ref", "refs/pull/8/head", "HEAD")
		run(t, repo, "git", "checkout", "-q", "main")
		pollOnce(t, m)
		runs := runsOf(t, eng)
		if len(runs) != 1 {
			t.Fatalf("%s: %d runs", how, len(runs))
		}
		d, _ := eng.GetRun(runs[0].ID)
		var steps []string
		for _, s := range d.StepList {
			steps = append(steps, s.ID)
		}
		return d.Restriction, steps
	}
	hr, hs := restrictionOf(t, "hosted")
	or, os := restrictionOf(t, "outside")
	for _, c := range []struct {
		how   string
		r     *engine.Restriction
		steps []string
	}{{"hosted", hr, hs}, {"outside", or, os}} {
		if c.r == nil || c.r.Code != engine.RestrictFork {
			t.Fatalf("%s: not restricted as a fork: %+v", c.how, c.r)
		}
		for _, s := range c.steps {
			if s == "evil" {
				t.Fatalf("%s: a fork's run used the pipeline it brought", c.how)
			}
		}
	}
	if strings.Join(hs, ",") != strings.Join(os, ",") {
		t.Fatalf("the two ways ran different pipelines: %v and %v", hs, os)
	}
}
