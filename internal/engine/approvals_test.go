package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const gated = `
pipeline: demo
steps:
  build: {image: x, run: b}
  approve:
    kind: approval
    needs: build
    when: {branch: main}
    message: Deploy to production?
  deploy: {image: x, run: d, needs: approve}
`

var onMain = Trigger{Event: "push", Branch: "main", Ref: "refs/heads/main", SHA: "0123456789abcdef", Actor: "dana", Message: "Fix the login page"}

// toApproval creates a gated run and runs build, so approve is waiting.
func toApproval(t *testing.T, e *Engine) string {
	t.Helper()
	id := create(t, e, gated, onMain)
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "running", "build", "succeeded", "approve", "waiting", "deploy", "pending")
	return id
}

func stepOf(t *testing.T, e *Engine, runID, stepID string) StepView {
	t.Helper()
	d, err := e.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.StepList {
		if s.ID == stepID {
			return s
		}
	}
	t.Fatalf("no step %s", stepID)
	return StepView{}
}

func countEvents(t *testing.T, e *Engine, runID string, types ...EventType) int {
	t.Helper()
	evs, err := e.Events(runID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range evs {
		for _, typ := range types {
			if ev.Type == typ {
				n++
			}
		}
	}
	return n
}

func TestApprovalApproved(t *testing.T) {
	e, clk := newEngine(t)
	id := toApproval(t, e)
	none(t, e) // an approval is never handed to a runner
	s := stepOf(t, e, id, "approve")
	start := clk.Now().UnixMilli()
	if s.Kind != "approval" || s.Message != "Deploy to production?" || s.Attempt != 1 || s.QueuedAt != start ||
		s.Deadline != start+(24*time.Hour).Milliseconds() || s.Container != "" || s.Image != "" {
		t.Fatalf("waiting step %+v", s)
	}
	if r, _ := e.runSummary(id); len(r.Waiting) != 1 || r.Waiting[0] != "approve" {
		t.Fatalf("waiting %v", r.Waiting)
	}

	clk.Add(time.Minute)
	if err := e.Decide(id, "approve", true, "sam", "  looks good "); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "running", "approve", "succeeded", "deploy", "queued")
	s = stepOf(t, e, id, "approve")
	if s.DecidedBy != "sam" || s.DecidedAt != clk.Now().UnixMilli() || s.Comment != "looks good" || s.Error != "" {
		t.Fatalf("decision %+v", s)
	}
	if r, _ := e.runSummary(id); len(r.Waiting) != 0 {
		t.Fatalf("still waiting %v", r.Waiting)
	}
	evs, _ := e.Events(id)
	var approved *Event
	for i := range evs {
		if evs[i].Type == StepApproved {
			approved = &evs[i]
		}
	}
	if approved == nil || approved.Data.Actor != "sam" || approved.Data.Comment != "looks good" || approved.At != clk.Now().UnixMilli() {
		t.Fatalf("approved event %+v", approved)
	}

	// Deciding again, or deciding something that is not an approval, is a conflict.
	if err := e.Decide(id, "approve", false, "dana", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("second decision: %v", err)
	}
	if err := e.Decide(id, "build", true, "sam", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("approving a plain step: %v", err)
	}
	if err := e.Decide(id, "nope", true, "sam", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approving a missing step: %v", err)
	}
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded", "deploy", "succeeded")
}

func TestApprovalRejectedThenResumed(t *testing.T) {
	e, clk := newEngine(t)
	id := toApproval(t, e)
	if err := e.Decide(id, "approve", false, "dana", "not today"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "failed", "approve", "failed", "deploy", "skipped")
	s := stepOf(t, e, id, "approve")
	if s.Error != "rejected by dana: not today" || s.DecidedBy != "dana" || s.Comment != "not today" {
		t.Fatalf("rejected step %+v", s)
	}
	if d := stepOf(t, e, id, "deploy"); d.SkipKind != "upstream" {
		t.Fatalf("deploy %+v", d)
	}

	// Resuming makes it wait again, with a fresh deadline and no old decision.
	clk.Add(3 * time.Hour)
	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "running", "build", "succeeded", "approve", "waiting", "deploy", "pending")
	s = stepOf(t, e, id, "approve")
	if s.Attempt != 2 || s.Deadline != clk.Now().Add(24*time.Hour).UnixMilli() || s.DecidedBy != "" || s.Comment != "" || s.Error != "" {
		t.Fatalf("after resume %+v", s)
	}
	if err := e.Decide(id, "approve", true, "sam", ""); err != nil {
		t.Fatal(err)
	}
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded")
	if n := countEvents(t, e, id, StepLeased); n != 2 {
		t.Fatalf("%d steps leased, want build and deploy once each", n)
	}
}

func TestApprovalExpires(t *testing.T) {
	e, clk := newEngine(t)
	id := toApproval(t, e)
	clk.Add(24*time.Hour - time.Second)
	e.expireWaits()
	want(t, statuses(t, e, id), "approve", "waiting")

	clk.Add(time.Second)
	// Once the deadline has passed only expiry decides, even before a
	// reaper has got round to it.
	if err := e.Decide(id, "approve", true, "sam", ""); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "nobody decided within 24h") {
		t.Fatalf("deciding after the deadline: %v", err)
	}
	e.expireWaits()
	want(t, statuses(t, e, id), "run", "failed", "approve", "failed", "deploy", "skipped")
	s := stepOf(t, e, id, "approve")
	if s.Error != "nobody decided within 24h" || s.DecidedBy != "" {
		t.Fatalf("expired step %+v", s)
	}
	e.expireWaits()
	if n := countEvents(t, e, id, StepRejected); n != 1 {
		t.Fatalf("%d rejections, want 1", n)
	}
}

func TestCancelWhileWaiting(t *testing.T) {
	e, _ := newEngine(t)
	id := toApproval(t, e)
	if err := e.Cancel(id, "dana"); err != nil {
		t.Fatal(err)
	}
	// Nothing is running, so the run is canceled at once.
	want(t, statuses(t, e, id), "run", "canceled", "approve", "canceled", "deploy", "canceled")
	if err := e.Decide(id, "approve", true, "sam", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("deciding a canceled approval: %v", err)
	}
	if err := e.Resume(id, "dana"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "running", "approve", "waiting", "deploy", "pending")
}

func TestApprovalSkippedWhenConditionFails(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, gated, Trigger{Event: "push", Branch: "feature/x"})
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "succeeded", "approve", "skipped", "deploy", "skipped")
	if n := countEvents(t, e, id, StepWaiting); n != 0 {
		t.Fatalf("%d step.waiting events, want none", n)
	}
	if s := stepOf(t, e, id, "approve"); s.SkipKind != "condition" {
		t.Fatalf("approve %+v", s)
	}
}

// A run whose only unfinished step is waiting is not finished.
func TestRunWaitingIsNotFinished(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  a: {image: x, run: a}
  ok: {kind: approval, message: "Ship it?"}
`, Trigger{Actor: "dana"})
	want(t, statuses(t, e, id), "run", "running", "a", "queued", "ok", "waiting")
	ok(t, e, take(t, e), nil)
	want(t, statuses(t, e, id), "run", "running", "a", "succeeded", "ok", "waiting")
	runs, err := e.ListRuns(RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status.Terminal() || runs[0].FinishedAt != 0 || len(runs[0].Waiting) != 1 || runs[0].Waiting[0] != "ok" {
		t.Fatalf("runs %+v", runs[0])
	}
	if err := e.Decide(id, "ok", true, "sam", ""); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "succeeded")
}

func TestSelfApproval(t *testing.T) {
	no := false
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, AllowSelfApproval: &no})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	id := toApproval(t, e) // started by dana
	err = e.Decide(id, "approve", true, "dana", "")
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "dana started this run, so someone else must approve it") {
		t.Fatalf("self-approval: %v", err)
	}
	want(t, statuses(t, e, id), "approve", "waiting")
	if err := e.Decide(id, "approve", true, "sam", ""); err != nil {
		t.Fatalf("someone else approving: %v", err)
	}
	ok(t, e, take(t, e), nil) // deploy

	// Rejecting your own run is always allowed.
	id = toApproval(t, e)
	if err := e.Decide(id, "approve", false, "dana", "changed my mind"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "approve", "failed")

	// By default, self-approval is allowed: there is no sign-in yet.
	d, _ := newEngine(t)
	id = toApproval(t, d)
	if err := d.Decide(id, "approve", true, "dana", ""); err != nil {
		t.Fatalf("self-approval by default: %v", err)
	}
	if err := d.Decide(id, "approve", true, " ", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no actor: %v", err)
	}
}

func TestApprovalsList(t *testing.T) {
	e, clk := newEngine(t)
	if list, err := e.Approvals(); err != nil || len(list) != 0 {
		t.Fatalf("empty list %v %v", list, err)
	}
	first := toApproval(t, e)
	clk.Add(time.Minute)
	second := create(t, e, `
pipeline: demo
steps:
  one: {kind: approval, message: "First question?"}
  two: {kind: approval, message: "Second question?", timeout: 1h}
`, Trigger{Event: "manual", Branch: "release/2", SHA: "fedcba9876543210", Actor: "sam", Message: "Release 2"})
	create(t, e, gated, Trigger{Event: "push", Branch: "feature/x"}) // build queued, nothing waiting

	list, err := e.Approvals()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, a := range list {
		got = append(got, a.RunID+"/"+a.StepID)
	}
	if strings.Join(got, " ") != second+"/one "+second+"/two "+first+"/approve" {
		t.Fatalf("order %v", got)
	}
	at := clk.Now().UnixMilli()
	two := list[1]
	if two.Project != "demo" || two.RunNumber != 2 || two.Message != "Second question?" || two.StartedBy != "sam" ||
		two.CommitSubject != "Release 2" || two.ShortSHA != "fedcba98" || two.Branch != "release/2" ||
		two.WaitingSince != at || two.Deadline != at+time.Hour.Milliseconds() {
		t.Fatalf("approval %+v", two)
	}
	if a := list[2]; a.Message != "Deploy to production?" || a.StartedBy != "dana" || a.RunNumber != 1 {
		t.Fatalf("approval %+v", a)
	}

	if err := e.Decide(second, "one", false, "dana", ""); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.Approvals(); len(list) != 2 || list[0].StepID != "two" {
		t.Fatalf("after a decision %+v", list)
	}
}

// Two servers: one approves while the other rejects. Exactly one decision
// is recorded, and the other caller is told it lost.
func TestConcurrentDecisionsHaveOneOutcome(t *testing.T) {
	a, b, _ := pair(t)
	for i := 0; i < 10; i++ {
		id := create(t, a, lone, Trigger{Actor: "dana"})
		errs := make(chan error, 2)
		go func() { errs <- a.Decide(id, "ok", true, "sam", "") }()
		go func() { errs <- b.Decide(id, "ok", false, "dana", "") }()
		e1, e2 := <-errs, <-errs
		if (e1 == nil) == (e2 == nil) {
			t.Fatalf("want exactly one decision to succeed: %v, %v", e1, e2)
		}
		if e := errors.Join(e1, e2); !errors.Is(e, ErrConflict) {
			t.Fatalf("the losing decision should be a conflict: %v", e)
		}
		if n := countEvents(t, a, id, StepApproved, StepRejected); n != 1 {
			t.Fatalf("%d decision events, want 1", n)
		}
		statuses(t, b, id) // the tables agree with the events
	}
}

const lone = "pipeline: demo\nsteps:\n  ok: {kind: approval, message: \"Ship it?\"}\n"

// Every server expires approvals; each is still rejected exactly once, and
// a decision at the same moment never adds a second outcome.
func TestExpiryHappensOnce(t *testing.T) {
	a, b, clk := pair(t)
	expiring := create(t, a, lone, Trigger{Actor: "dana"})
	racing := create(t, a, lone, Trigger{Actor: "dana"})
	clk.Add(24 * time.Hour)
	var errs [4]error
	done := make(chan struct{})
	for i, e := range []*Engine{a, b, a, b} {
		go func() {
			defer func() { done <- struct{}{} }()
			e.expireWaits()
			errs[i] = e.Decide(racing, "ok", i%2 == 0, "sam", "")
		}()
	}
	for range errs {
		<-done
	}
	for _, err := range errs {
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("a decision after the deadline: %v", err)
		}
	}
	for _, id := range []string{expiring, racing} {
		if n := countEvents(t, a, id, StepApproved, StepRejected); n != 1 {
			t.Fatalf("%d decision events, want 1", n)
		}
		want(t, statuses(t, b, id), "run", "failed", "ok", "failed")
		if s := stepOf(t, b, id, "ok"); s.Error != "nobody decided within 24h" {
			t.Fatalf("step %+v", s)
		}
	}
}

// A decision made through one server reaches browsers connected to another.
func TestDecisionReachesOtherServer(t *testing.T) {
	a, b, _ := pair(t)
	id := create(t, a, lone, Trigger{Actor: "dana"})
	ch, stop := b.Hub().Subscribe(nil)
	defer stop()
	if err := a.Decide(id, "ok", true, "sam", "go"); err != nil {
		t.Fatal(err)
	}
	var sawStep, sawRun bool
	timeout := time.After(5 * time.Second)
	for !sawStep || !sawRun {
		select {
		case m := <-ch:
			switch d := m.Data.(type) {
			case *StepView:
				if d.ID == "ok" && d.Status == StepSucceeded && d.DecidedBy == "sam" && d.Comment == "go" {
					sawStep = true
				}
			case *RunSummary:
				if d.ID == id && d.Status == RunSucceeded && len(d.Waiting) == 0 {
					sawRun = true
				}
			}
		case <-timeout:
			t.Fatalf("the decision did not reach the other server: step %v, run %v", sawStep, sawRun)
		}
	}
	if list, err := b.Approvals(); err != nil || len(list) != 0 {
		t.Fatalf("approvals on the other server %+v %v", list, err)
	}
}
