package source

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func prHead(t *testing.T, repo, n string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "refs/pull/"+n+"/head").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// A pull request of a repository on an outside host starts a run that carries what the host said about it; a pull
// request from a fork is restricted, as an automation account's is, and runs the default branch's pipeline.
func TestOutsidePullRequestMetadataReachesTheRun(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	m.SetOutsideSource(fakeOutside{pr: func(n int) (PullRequestInfo, bool, error) {
		return PullRequestInfo{Number: n, Title: "Fix the retry", Author: "dana", Draft: true, URL: "https://git.example.com/acme/app/pull/3", Base: "main"}, true, nil
	}})
	pollOnce(t, m) // history is recorded, nothing runs
	openPR(t, repo, "3", "", "from a branch")
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 {
		t.Fatalf("%d runs", len(runs))
	}
	st, _ := eng.State(runs[0].ID)
	tr := st.Trigger
	if tr.PullRequest != 3 || tr.PullRequestTitle != "Fix the retry" || tr.PullRequestAuthor != "dana" || !tr.PullRequestDraft || tr.Fork ||
		tr.PullRequestURL != "https://git.example.com/acme/app/pull/3" {
		t.Fatalf("trigger %+v", tr)
	}
	if st.Restriction != nil {
		t.Fatalf("a pull request from the repository itself was restricted: %+v", st.Restriction)
	}
	d, err := eng.GetRun(runs[0].ID)
	if err != nil || d.TriggerDetail == nil || d.TriggerDetail.PullRequestTitle != "Fix the retry" || !d.TriggerDetail.PullRequestDraft {
		t.Fatalf("the run page's detail: %+v %v", d.TriggerDetail, err)
	}

	// From a fork: the same, but restricted.
	m.SetOutsideSource(fakeOutside{pr: func(n int) (PullRequestInfo, bool, error) {
		return PullRequestInfo{Number: n, Title: "Sneaky", Author: "mallory", Fork: true}, true, nil
	}})
	openPR(t, repo, "4", "", "from a fork")
	pollOnce(t, m)
	runs = runsOf(t, eng)
	st, _ = eng.State(runs[0].ID)
	if runs[0].PullRequest != 4 || st.Restriction == nil || st.Restriction.Code != engine.RestrictFork || !st.Trigger.Fork {
		t.Fatalf("a fork pull request was not restricted: %+v %+v", runs[0], st.Restriction)
	}
}

// What a webhook or a poll said is used for its head, without asking the host again, and a stale note is not taken
// for a newer head.
func TestOutsidePullRequestNotesAreForTheirHeadOnly(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	asked := 0
	m.SetOutsideSource(fakeOutside{pr: func(n int) (PullRequestInfo, bool, error) {
		asked++
		return PullRequestInfo{}, true, errors.New("the host is down")
	}})
	pollOnce(t, m)
	openPR(t, repo, "5", "", "first head")
	m.NotePullRequest("app", PullRequestInfo{Number: 5, Head: prHead(t, repo, "5"), Title: "Noted", Author: "ana", Fork: true})
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 || asked != 0 {
		t.Fatalf("runs %d, asked %d", len(runs), asked)
	}
	if st, _ := eng.State(runs[0].ID); st.Trigger.PullRequestTitle != "Noted" || st.Restriction == nil {
		t.Fatalf("the note was not used: %+v", st.Trigger)
	}
	// A new head: the old note does not apply, the host is asked, cannot answer, and nothing runs (fail closed).
	openPR(t, repo, "5", "", "second head")
	pollOnce(t, m)
	if len(runsOf(t, eng)) != 1 || asked == 0 {
		t.Fatalf("a pull request ran without the host saying whether it is a fork (asked %d)", asked)
	}
	if s, _ := m.Get("app"); !strings.Contains(s.Decisions[0].Detail, "whether it is from a fork") {
		t.Fatalf("decision %+v", s.Decisions[0])
	}
}
