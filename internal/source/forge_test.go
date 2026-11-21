package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const intoPipeline = `
pipeline: app
on:
  pull_request: { into: [main, "release/*"] }
steps:
  test: {image: alpine, run: echo "into $LAUNCHWAY_PULL_REQUEST_INTO"}
`

// fakeForge answers from a table, counts what it is asked, and can be made
// to fail.
type fakeForge struct {
	mu      sync.Mutex
	base    map[int]string
	head    map[int]string
	down    error
	asked   int
	askedAt map[int]int
}

func newFakeForge() *fakeForge {
	return &fakeForge{base: map[int]string{}, head: map[int]string{}, askedAt: map[int]int{}}
}

func (f *fakeForge) PullRequest(_ context.Context, _ string, n int) (PullRequestInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	f.askedAt[n]++
	if f.down != nil {
		return PullRequestInfo{}, f.down
	}
	return PullRequestInfo{Number: n, Base: f.base[n], Head: f.head[n]}, nil
}

func (f *fakeForge) set(fn func(f *fakeForge)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

func decisions(t *testing.T, eng *engine.Engine) []engine.PushDecision {
	t.Helper()
	ds, err := eng.PushDecisions("app")
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func seedPR(t *testing.T, repo string, m *Manager) {
	t.Helper()
	pollOnce(t, m) // history is seeded, not run
}

// A pipeline with into runs for a pull request into main, and not for one
// into staging, and the decision says why.
func TestPullRequestsIntoRunOnlyForTheirTargets(t *testing.T) {
	dir, repo := prRepo(t, intoPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	ff := newFakeForge()
	if err := m.SetForge("app", ff); err != nil {
		t.Fatal(err)
	}
	seedPR(t, repo, m)
	openPR(t, repo, "1", "", "into main")
	openPR(t, repo, "2", "", "into staging")
	openPR(t, repo, "3", "", "into a release branch")
	ff.set(func(f *fakeForge) { f.base[1], f.base[2], f.base[3] = "main", "staging", "release/1.2" })
	pollOnce(t, m)

	runs := runsOf(t, eng)
	if len(runs) != 2 {
		t.Fatalf("%d runs, want 2 (pull requests 1 and 3): %+v", len(runs), runs)
	}
	targets := map[int]string{}
	for _, r := range runs {
		targets[r.PullRequest] = r.PullRequestInto
	}
	if targets[1] != "main" || targets[3] != "release/1.2" || targets[2] != "" {
		t.Fatalf("targets %v", targets)
	}
	var skipped *engine.PushDecision
	for _, d := range decisions(t, eng) {
		if d.Ref == "refs/pull/2/head" {
			skipped = &d
		}
	}
	if skipped == nil || skipped.Outcome != "ignored" ||
		skipped.Detail != "pull request #2 not run: it targets staging, and the pipeline runs for pull requests into main, release/*" {
		t.Fatalf("%+v", skipped)
	}
	// The run says where it goes, and exports it.
	for _, r := range runs {
		if r.PullRequest == 1 {
			if tr, err := eng.RunTrigger(r.ID); err != nil || tr.PullRequestInto != "main" {
				t.Fatalf("%+v %v", tr, err)
			}
			j, err := eng.Lease(context.Background(), engine.RunnerInfo{ID: "r"}, 0)
			if err != nil || j == nil || j.Env["LAUNCHWAY_PULL_REQUEST_INTO"] == "" {
				t.Fatalf("%+v %v", j, err)
			}
		}
	}
	// A poll that sees nothing new asks the host nothing.
	before := ff.asked
	pollOnce(t, m)
	if ff.asked != before {
		t.Fatalf("asked the forge again for heads that were decided: %d -> %d", before, ff.asked)
	}
}

// Without a forge connection, nothing runs and the decision says so, once.
// The head is not marked seen, so connecting a forge later runs it.
func TestPullRequestsIntoWithoutAForgeRunNothing(t *testing.T) {
	dir, repo := prRepo(t, intoPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	seedPR(t, repo, m)
	openPR(t, repo, "12", "", "a change")
	pollOnce(t, m)
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("%d runs", len(runs))
	}
	ds := decisions(t, eng)
	if len(ds) != 1 || ds[0].Outcome != "waiting" ||
		!strings.HasPrefix(ds[0].Detail, "waiting: pull request #12 not run: the pipeline filters by target branch and the project has no forge connection") {
		t.Fatalf("%+v", ds)
	}
	if m.HasForge("app") {
		t.Fatal("HasForge")
	}
	seen, err := eng.SeenRefs("app")
	if err != nil {
		t.Fatal(err)
	}
	if _, marked := seen["refs/pull/12/head"]; marked {
		t.Fatalf("the head was marked seen: %v", seen)
	}
	ff := newFakeForge()
	ff.set(func(f *fakeForge) { f.base[12] = "main" })
	if err := m.SetForge("app", ff); err != nil {
		t.Fatal(err)
	}
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 1 {
		t.Fatalf("%d runs after the forge was added", len(runs))
	}
}

// With the forge down the pull request is not run, the decision says so
// once, and it runs after recovery. It never runs unfiltered.
func TestPullRequestsIntoFailClosedAndRecover(t *testing.T) {
	dir, repo := prRepo(t, intoPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	ff := newFakeForge()
	m.SetForge("app", ff)
	seedPR(t, repo, m)
	openPR(t, repo, "5", "", "a change")
	ff.set(func(f *fakeForge) {
		f.down = errors.New("Get https://git.example/api: connection refused")
		f.base[5] = "main"
	})
	for range 3 {
		pollOnce(t, m)
	}
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("a run started while the forge was down: %+v", runs)
	}
	ds := decisions(t, eng)
	if len(ds) != 1 || ds[0].Outcome != "waiting" || !strings.HasPrefix(ds[0].Detail, "waiting: forge unreachable") {
		t.Fatalf("one decision, not one for each poll: %+v", ds)
	}
	if seen, _ := eng.SeenRefs("app"); seen["refs/pull/5/head"] != "" {
		t.Fatalf("the head was claimed while the forge was down: %v", seen)
	}
	ff.set(func(f *fakeForge) { f.down = nil })
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 || runs[0].PullRequest != 5 || runs[0].PullRequestInto != "main" {
		t.Fatalf("after recovery: %+v", runs)
	}
}

// A forge that answers without a target, or about another head, is no
// answer.
func TestPullRequestsIntoNeedAnAnswerAboutThisHead(t *testing.T) {
	dir, repo := prRepo(t, intoPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	ff := newFakeForge()
	m.SetForge("app", ff)
	seedPR(t, repo, m)
	openPR(t, repo, "6", "", "a change")
	pollOnce(t, m) // no base
	ff.set(func(f *fakeForge) { f.base[6], f.head[6] = "main", "0000000000000000000000000000000000000000" })
	pollOnce(t, m) // another head
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("%+v", runs)
	}
	ds := decisions(t, eng)
	if len(ds) != 2 || !strings.Contains(ds[0].Detail, "another head") || !strings.Contains(ds[1].Detail, "did not say which branch") {
		t.Fatalf("%+v", ds)
	}
	ff.set(func(f *fakeForge) { f.head[6] = "" })
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 1 {
		t.Fatalf("%+v", runs)
	}
}

// A retargeted pull request is read again when its head moves, and the
// decision says the target changed.
func TestPullRequestsIntoReadAgainWhenTheHeadMoves(t *testing.T) {
	dir, repo := prRepo(t, intoPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	ff := newFakeForge()
	m.SetForge("app", ff)
	seedPR(t, repo, m)
	openPR(t, repo, "7", "", "seven one")
	ff.set(func(f *fakeForge) { f.base[7] = "staging" })
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("%+v", runs)
	}
	// Retargeted, and pushed to.
	ff.set(func(f *fakeForge) { f.base[7] = "main" })
	openPR(t, repo, "7", "", "second")
	pollOnce(t, m)
	runs := runsOf(t, eng)
	if len(runs) != 1 || runs[0].PullRequestInto != "main" || ff.askedAt[7] != 2 {
		t.Fatalf("%+v asked %d", runs, ff.askedAt[7])
	}
	var started string
	for _, d := range decisions(t, eng) {
		if d.Outcome == "started" {
			started = d.Detail
		}
	}
	if !strings.Contains(started, "into main") || !strings.Contains(started, "the target changed from staging to main") {
		t.Fatalf("%q", started)
	}
	// And the other way: it moves out of what the pipeline runs for.
	ff.set(func(f *fakeForge) { f.base[7] = "docs" })
	openPR(t, repo, "7", "", "third")
	pollOnce(t, m)
	if len(runsOf(t, eng)) != 1 {
		t.Fatal("a pull request retargeted away from main ran")
	}
	if ds := decisions(t, eng); !strings.Contains(ds[0].Detail, "it targets docs") || !strings.Contains(ds[0].Detail, "the target changed from main") {
		t.Fatalf("%+v", ds[0])
	}
}

// pull_request: {} behaves as it always did: no forge asked, no filter.
func TestPullRequestWithoutIntoNeedsNoForge(t *testing.T) {
	dir, repo := prRepo(t, prPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	ff := newFakeForge()
	ff.down = fmt.Errorf("never asked")
	m.SetForge("app", ff)
	seedPR(t, repo, m)
	openPR(t, repo, "1", "", "a change")
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 1 || runs[0].PullRequestInto != "" || ff.asked != 0 {
		t.Fatalf("%+v asked %d", runs, ff.asked)
	}
}

// Only the default branch's pipeline filters: a pull request that adds an
// into to its own pipeline does not turn the filter off for itself or on.
func TestPullRequestIntoComesFromTheDefaultBranch(t *testing.T) {
	dir, repo := prRepo(t, intoPipeline)
	m, eng := prManager(t, testdb.New(t), dir, Project{Name: "app", URL: repo})
	ff := newFakeForge()
	m.SetForge("app", ff)
	seedPR(t, repo, m)
	// This pull request's own pipeline asks for every pull request.
	openPR(t, repo, "8", strings.ReplaceAll(intoPipeline, `{ into: [main, "release/*"] }`, "{}"), "widen it")
	ff.set(func(f *fakeForge) { f.base[8] = "staging" })
	pollOnce(t, m)
	if runs := runsOf(t, eng); len(runs) != 0 {
		t.Fatalf("a pull request widened its own filter: %+v", runs)
	}
}
