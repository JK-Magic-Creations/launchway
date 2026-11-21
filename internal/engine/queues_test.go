package engine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func queueEngine(t *testing.T, q QueueSettings) (*Engine, *clock) {
	t.Helper()
	clk := newClock()
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Queues: q})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, clk
}

const prPipeline = "pipeline: demo\non: { pull_request: {}, push: { branches: [main] } }\nsteps:\n  a: {image: x, run: y}\n"

func prRun(t *testing.T, e *Engine, n int, automation bool) *RunSummary {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, prPipeline), Automation: automation, Source: Source{Kind: "dir", Path: "/tmp"},
		Trigger: Trigger{Event: "pull_request", Ref: fmt.Sprintf("refs/pull/%d/head", n), PullRequest: n}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pushRun(t *testing.T, e *Engine, protected bool) *RunSummary {
	t.Helper()
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, prPipeline), Protected: protected, Source: Source{Kind: "dir", Path: "/tmp"},
		Trigger: Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func heldNumbers(t *testing.T, e *Engine) []int {
	t.Helper()
	q, err := e.Queue("")
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, x := range q {
		out = append(out, x.Number)
	}
	return out
}

// Eight pull requests from automation arrive at once: only two go, the rest
// are held with a reason that names what they wait behind, and the main
// build is never held. When a run ends the next in order starts.
func TestBurstOfAutomationPullRequestsIsHeldAndTheMainBuildGoes(t *testing.T) {
	e, _ := queueEngine(t, QueueSettings{})
	var runs []*RunSummary
	for i := 1; i <= 8; i++ {
		runs = append(runs, prRun(t, e, i, true))
	}
	if got := heldNumbers(t, e); fmt.Sprint(got) != "[3 4 5 6 7 8]" {
		t.Fatalf("held %v", got)
	}
	q, _ := e.Queue("")
	if q[0].Code != HoldAutomationLimit || !strings.Contains(q[0].Reason, "allows 2 runs started by automation accounts at once") || fmt.Sprint(q[0].Behind) != "[1 2]" {
		t.Fatalf("%+v", q[0])
	}
	if !strings.Contains(q[1].Reason, "1 held run go first") {
		t.Fatalf("second: %q", q[1].Reason)
	}
	main := pushRun(t, e, true)
	if d, _ := e.GetRun(main.ID); d.Queue == nil || d.Queue.Held {
		t.Fatalf("the main build is held: %+v", d.Queue)
	}
	// Only the runs that go have steps to take.
	j := take(t, e)
	ok(t, e, j, nil)
	if got := heldNumbers(t, e); fmt.Sprint(got) != "[4 5 6 7 8]" {
		t.Fatalf("after one ended, held %v", got)
	}
	if d, _ := e.GetRun(runs[2].ID); d.Queue.Held {
		t.Fatal("run 3 should have started")
	}
}

// A new commit on the same pull request cancels the older run, with a plain
// reason, by default; supersede: false in the pipeline keeps every run.
func TestPullRequestRunsSupersedeByDefault(t *testing.T) {
	e, _ := queueEngine(t, QueueSettings{})
	first := prRun(t, e, 7, false)
	second := prRun(t, e, 7, false)
	d, err := e.GetRun(first.ID)
	if err != nil || d.Status != RunCanceled || !strings.Contains(d.CancelReason, "a newer commit was pushed to the same pull request") || d.SupersededByNumber != second.Number {
		t.Fatalf("%v %s %q", err, d.Status, d.CancelReason)
	}
	off := strings.Replace(prPipeline, "pipeline: demo\n", "pipeline: demo\nsupersede: false\n", 1)
	mk := func() *RunSummary {
		r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, off), Source: Source{Kind: "dir", Path: "/tmp"},
			Trigger: Trigger{Event: "pull_request", Ref: "refs/pull/9/head", PullRequest: 9}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, _ := mk(), mk()
	if d, _ := e.GetRun(a.ID); d.Status == RunCanceled {
		t.Fatal("supersede: false was not honored")
	}
}

// When room is short, a held push goes before a held pull request, whichever
// came first; an operator can move a run to the front, hold it, and release it.
func TestHeldRunsGoByPriorityAndOperatorActions(t *testing.T) {
	e, _ := queueEngine(t, QueueSettings{PullRequestRuns: 1})
	running := prRun(t, e, 1, false)
	pr := prRun(t, e, 2, false)
	if got := heldNumbers(t, e); fmt.Sprint(got) != "[2]" {
		t.Fatalf("held %v", got)
	}
	// A manual-kind limit of 1 holds a second manual run.
	d, _ := e.GetRun(pr.ID)
	names := map[string]bool{}
	for _, a := range d.Actions {
		names[a.Name] = a.Available
	}
	if !names[QueueRunNext] || !names[QueueHold] || names[QueueRelease] {
		t.Fatalf("actions %+v", d.Actions)
	}
	if err := e.QueueAction(pr.ID, QueueHold, "ada"); err != nil {
		t.Fatal(err)
	}
	q, _ := e.Queue("")
	if q[0].Code != HoldByOperator || !strings.Contains(q[0].Reason, "held by ada") {
		t.Fatalf("%+v", q[0])
	}
	// Finishing the running one does not let a held-by-person run go.
	ok(t, e, take(t, e), nil)
	if d, _ := e.GetRun(pr.ID); !d.Queue.Held {
		t.Fatal("a run held by a person went by itself")
	}
	if err := e.QueueAction(pr.ID, QueueRelease, "ada"); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.GetRun(pr.ID); d.Queue.Held {
		t.Fatal("released, with room free, it should go")
	}
	if err := e.QueueAction(running.ID, QueueHold, "ada"); err == nil || !strings.Contains(err.Error(), "not waiting in a queue") {
		t.Fatalf("holding a run that is not waiting: %v", err)
	}
}

func TestQueueOrderPutsPushBeforePullRequestAndBumpFirst(t *testing.T) {
	e, _ := queueEngine(t, QueueSettings{PullRequestRuns: 1, PushRuns: 1})
	prRun(t, e, 1, false) // goes
	pushRun(t, e, false)  // goes (one push allowed)
	pr2 := prRun(t, e, 2, false)
	push2 := pushRun(t, e, false)
	pr3 := prRun(t, e, 3, false)
	q, _ := e.Queue("")
	var order []int
	for _, x := range q {
		order = append(order, x.Number)
	}
	if fmt.Sprint(order) != fmt.Sprint([]int{push2.Number, pr2.Number, pr3.Number}) {
		t.Fatalf("order %v", order)
	}
	if err := e.QueueAction(pr3.ID, QueueRunNext, "ada"); err != nil {
		t.Fatal(err)
	}
	q, _ = e.Queue("")
	if q[0].Number != pr3.Number {
		t.Fatalf("run next did not put #%d first: %+v", pr3.Number, q[0])
	}
}

// A project's own setting replaces the installation's, key by key.
func TestProjectQueueSettingsOverride(t *testing.T) {
	e, _ := queueEngine(t, QueueSettings{PullRequestRuns: 1, Projects: map[string]QueueSettings{"demo": {PullRequestRuns: -1}}})
	for i := 1; i <= 6; i++ {
		prRun(t, e, i, false)
	}
	if got := heldNumbers(t, e); len(got) != 0 {
		t.Fatalf("no limit for this project, yet held %v", got)
	}
}
