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
