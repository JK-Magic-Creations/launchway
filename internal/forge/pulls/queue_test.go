package pulls

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// shaRuns is a run source whose answer depends on the head: status of a
// pipeline named test for each commit (a commit with no entry has no run).
type shaRuns struct {
	mu     sync.Mutex
	status map[string]string
}

func (f *shaRuns) set(sha, status string) { f.mu.Lock(); f.status[sha] = status; f.mu.Unlock() }

func (f *shaRuns) RunsOf(_ context.Context, _, _ string, _ int) ([]RunInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []RunInfo
	for sha, st := range f.status {
		out = append(out, RunInfo{ID: "run-" + sha[:8], Project: "p", Number: 1, Pipeline: "test", Status: st, SHA: sha, Created: 1})
	}
	return out, nil
}

type queueWorld struct {
	*world
	runs *shaRuns
}

func newQueueWorld(t *testing.T, names ...string) *queueWorld {
	t.Helper()
	w := newWorld(t)
	q := &queueWorld{world: w, runs: &shaRuns{status: map[string]string{}}}
	w.svc.Runs = q.runs
	w.svc.Paths = func(context.Context, string) (string, error) { return "ada/demo", nil }
	w.commit("main", "", map[string]string{"README.md": "hi\n"}, "base")
	if err := w.svc.Store.SetRule(w.ctx, repoID, Rule{Pattern: "main", RequiredChecks: []string{"test"}, MergeQueue: true}, "ada"); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		w.commit(n, "main", map[string]string{n + ".txt": n + "\n"}, "add "+n)
		w.open("ada", New{Title: "Add " + n, SourceBranch: n, TargetBranch: "main"})
	}
	w.svc.Settle()
	return q
}

func (q *queueWorld) enqueue(number int) (QueueEntry, error) {
	q.svc.Settle()
	return q.svc.Enqueue(q.ctx, repoID, "ada/demo", q.get(number), "merge", "ada", RoleOperator, 0)
}

func (q *queueWorld) advance() { q.svc.Settle(); q.svc.AdvanceQueue(q.ctx, repoID, "main"); q.svc.Settle() }

func (q *queueWorld) entry(number int) QueueEntry {
	e, _ := q.svc.Store.QueueEntryOf(q.ctx, q.get(number).ID)
	if e == nil {
		return QueueEntry{}
	}
	return *e
}

func TestEntriesMergeInOrderEachTestedOnTheTipPlusTheOnesAhead(t *testing.T) {
	q := newQueueWorld(t, "a", "b")
	q.runs.set(q.get(1).HeadSHA, "running")
	q.runs.set(q.get(2).HeadSHA, "running")
	if _, err := q.enqueue(1); err != nil {
		t.Fatal(err)
	}
	if e, err := q.enqueue(2); err != nil || e.Position != 2 {
		t.Fatalf("%+v %v", e, err)
	}
	if q.get(1).State != Open || q.entry(1).State != QueueTesting || q.entry(2).Position != 2 {
		t.Fatalf("nothing merges before the check passes: %+v %+v", q.entry(1), q.entry(2))
	}
	q.runs.set(q.get(1).HeadSHA, "succeeded")
	q.advance()
	if q.get(1).State != Merged {
		t.Fatalf("the first entry should merge: %+v", q.get(1))
	}
	// The second is brought up to date on the new tip and tested there: its old green check does not count.
	if q.entry(2).State != QueueTesting || q.entry(2).Position != 1 || q.entry(2).TestedSHA == "" {
		t.Fatalf("%+v", q.entry(2))
	}
	b := q.get(2)
	if b.State != Open || b.HeadSHA != q.entry(2).TestedSHA || b.Mergeability.Behind != 0 {
		t.Fatalf("%+v", b)
	}
	q.advance()
	if q.get(2).State != Open {
		t.Fatal("it merged on a head that was never tested")
	}
	q.runs.set(b.HeadSHA, "succeeded")
	q.advance()
	if q.get(2).State != Merged {
		t.Fatalf("%+v", q.entry(2))
	}
	tip := q.refs()["refs/heads/main"]
	for _, f := range []string{"a.txt", "b.txt", "README.md"} {
		if q.file(tip, f) == "" {
			t.Errorf("%s is missing from main", f)
		}
	}
	if q.entry(2).PullNumber != 0 {
		t.Errorf("a merged entry stays: %+v", q.entry(2))
	}
}

func TestAFailingMiddleEntryIsRemovedWithItsReasonAndTheRestGoOn(t *testing.T) {
	q := newQueueWorld(t, "a", "b", "c")
	for n := 1; n <= 3; n++ {
		q.runs.set(q.get(n).HeadSHA, "running")
		if _, err := q.enqueue(n); err != nil {
			t.Fatal(err)
		}
	}
	var failures []string
	q.svc.OnEvent = func(_ context.Context, e Event) {
		if e.Action == "queue_failed" {
			failures = append(failures, e.Detail["reason"])
		}
	}
	q.runs.set(q.get(1).HeadSHA, "succeeded")
	q.advance() // a merges; b is brought up to date
	q.runs.set(q.get(2).HeadSHA, "failed")
	q.advance() // b fails: removed; c is brought up to date
	if e := q.entry(2); e.State != QueueRemoved || !strings.Contains(e.Reason, "`test` failed") {
		t.Fatalf("%+v", e)
	}
	if len(failures) != 1 || !strings.Contains(failures[0], "failed") {
		t.Fatalf("the person was not told: %v", failures)
	}
	if q.entry(3).Position != 1 || q.entry(3).State != QueueTesting {
		t.Fatalf("%+v", q.entry(3))
	}
	q.runs.set(q.get(3).HeadSHA, "succeeded")
	q.advance()
	if q.get(3).State != Merged || q.get(2).State != Open {
		t.Fatalf("c %s b %s", q.get(3).State, q.get(2).State)
	}
	tip := q.refs()["refs/heads/main"]
	if q.file(tip, "b.txt") != "" || q.file(tip, "c.txt") == "" || q.file(tip, "a.txt") == "" {
		t.Fatal("the failed entry reached main, or a good one did not")
	}
}

func TestAForcePushWhileQueuedTakesTheEntryOut(t *testing.T) {
	q := newQueueWorld(t, "a", "b")
	q.runs.set(q.get(1).HeadSHA, "running")
	q.runs.set(q.get(2).HeadSHA, "running")
	_, _ = q.enqueue(1)
	_, _ = q.enqueue(2)
	old := q.get(2).HeadSHA
	evil := q.commit("b2", "main", map[string]string{"evil.txt": "x\n"}, "something else")
	q.force("b", evil)
	q.push("mallory", "b", old, evil)
	q.advance()
	e := q.entry(2)
	if e.State != QueueRemoved || !strings.Contains(e.Reason, "new commits were pushed") {
		t.Fatalf("%+v", e)
	}
	if e := q.entry(1); e.Position != 1 {
		t.Fatalf("the others keep their place: %+v", e)
	}
	// Queued again, it is a new entry at the back.
	q.runs.set(q.get(2).HeadSHA, "succeeded")
	if e, err := q.enqueue(2); err != nil || e.State == QueueRemoved {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestABranchWithAQueueRefusesDirectMergesButAdminsMayBypassWithAReason(t *testing.T) {
	q := newQueueWorld(t, "a")
	q.runs.set(q.get(1).HeadSHA, "succeeded")
	_, err := q.svc.Merge(q.ctx, repoID, q.get(1), MergeInput{Actor: "grace", ActorRole: RoleOperator, RepoPath: "ada/demo"})
	if err == nil || !strings.Contains(err.Error(), "merge queue") {
		t.Fatalf("%v", err)
	}
	if _, err := q.svc.Merge(q.ctx, repoID, q.get(1), MergeInput{Actor: "grace", ActorRole: RoleAdmin, Bypass: "outage", RepoPath: "ada/demo"}); err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueRefusesWhatCannotMergeAndWhatHasNoQueue(t *testing.T) {
	q := newQueueWorld(t, "a", "b")
	q.runs.set(q.get(1).HeadSHA, "failed")
	if _, err := q.enqueue(1); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("a failed check: %v", err)
	}
	draft := true
	_, _ = q.svc.Edit(q.ctx, repoID, q.get(2), Edit{Draft: &draft}, "ada")
	if _, err := q.enqueue(2); err == nil || !strings.Contains(err.Error(), "draft") {
		t.Fatalf("a draft: %v", err)
	}
	_ = q.svc.Store.SetRule(q.ctx, repoID, Rule{Pattern: "main", RequiredChecks: []string{"test"}}, "ada")
	if _, err := q.enqueue(1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no queue: %v", err)
	}
}

func TestDequeueIsTheCancelAndTheQueueNeedsApprovalsToStayTrue(t *testing.T) {
	q := newQueueWorld(t, "a", "b")
	_ = q.svc.Store.SetRule(q.ctx, repoID, Rule{Pattern: "main", RequiredChecks: []string{"test"}, MergeQueue: true, RequiredApprovals: 1}, "ada")
	q.runs.set(q.get(1).HeadSHA, "running")
	if _, err := q.enqueue(1); err == nil {
		t.Fatal("queued without the approval the rule asks for")
	}
	_, _ = q.svc.SubmitReview(q.ctx, repoID, q.get(1), "grace", ReviewInput{Verdict: VerdictApprove, Role: RoleOperator})
	if _, err := q.enqueue(1); err != nil {
		t.Fatal(err)
	}
	// Changes requested while it waits: out, with the reason.
	_, _ = q.svc.SubmitReview(q.ctx, repoID, q.get(1), "vera", ReviewInput{Verdict: VerdictRequestChanges, Role: RoleOperator})
	q.advance()
	if e := q.entry(1); e.State != QueueRemoved || !strings.Contains(e.Reason, "asked for changes") {
		t.Fatalf("%+v", e)
	}
	if err := q.svc.Dequeue(q.ctx, repoID, q.get(1), "ada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestTheQueueWaitsForARequiredCheckThatAPathFilterSkipped(t *testing.T) {
	q := newQueueWorld(t, "a")
	q.svc.Runs = &fakeRuns{runs: []RunInfo{{Project: "p", SHA: q.get(1).HeadSHA, PathSkipped: true}}}
	if _, err := q.enqueue(1); err != nil {
		t.Fatal(err)
	}
	if q.get(1).State != Merged {
		t.Fatalf("a skipped pipeline must not hold the queue forever: %+v", q.entry(1))
	}
}
