package engine

import (
	"strings"
	"testing"
)

const loopExtraPipeline = `
pipeline: matrix
steps:
  test:
    for_each: { python: ["3.12", "3.13"] }
    add: [ { python: "3.14" } ]
    continue_for: [ { python: "3.14" } ]
    fail_fast: true
    max_parallel: 3
    image: alpine
    run: pytest
  report:
    needs: test
    image: alpine
    run: echo report
`

// An added member runs like any other; one continue_for selects may fail:
// the others finish, the step that needs the loop runs, and the run succeeds
// with the member shown as failed (allowed).
func TestLoopContinueForLetsAMemberFail(t *testing.T) {
	e, _ := newEngine(t)
	id := createBound(t, e, loopExtraPipeline, nil, Trigger{Event: "manual"})
	a, b, c := take(t, e), take(t, e), take(t, e)
	if c.StepID != "test[python=3.14]" {
		t.Fatalf("the added member is third: %s", c.StepID)
	}
	fail(t, e, c)
	ok(t, e, a, nil)
	ok(t, e, b, nil)
	j := take(t, e)
	if j.StepID != "report" {
		t.Fatalf("report runs after the tolerated failure: %s", j.StepID)
	}
	ok(t, e, j, nil)
	d, _ := e.GetRun(id)
	if d.Status != RunSucceeded {
		t.Fatalf("run %s", d.Status)
	}
	s := stepOf(t, e, id, "test[python=3.14]")
	if s.Status != StepFailed || !s.AllowedFailure {
		t.Fatalf("%+v", s)
	}
	var extra, allowed int
	for _, l := range d.Graph.Loops {
		for _, m := range l.Members {
			if m.Extra {
				extra++
			}
			if m.AllowedFailure {
				allowed++
			}
		}
	}
	if extra != 1 || allowed != 1 {
		t.Fatalf("the run's loop shows %d extra and %d allowed members", extra, allowed)
	}
}

// A member continue_for does not select still stops the others.
func TestLoopContinueForDoesNotExcuseOtherMembers(t *testing.T) {
	e, _ := newEngine(t)
	id := createBound(t, e, loopExtraPipeline, nil, Trigger{Event: "manual"})
	a, b, c := take(t, e), take(t, e), take(t, e)
	fail(t, e, a)
	st := statuses(t, e, id)
	if st[b.StepID] != "canceled" || st[c.StepID] != "canceled" || st["run"] != "failed" {
		t.Fatalf("%v", st)
	}
	d, _ := e.GetRun(id)
	for _, s := range d.StepList {
		if s.ID == c.StepID && !strings.Contains(s.Error, "fail_fast") {
			t.Fatalf("reason: %q", s.Error)
		}
	}
}

// A tolerated failure of one member does not stop the rest even when fail_fast
// is on.
func TestLoopFailFastIgnoresATolerantMember(t *testing.T) {
	e, _ := newEngine(t)
	id := createBound(t, e, loopExtraPipeline, nil, Trigger{Event: "manual"})
	_, _, c := take(t, e), take(t, e), take(t, e)
	fail(t, e, c)
	st := statuses(t, e, id)
	if st["test[python=3.12]"] != "running" && st["test[python=3.12]"] != "leased" {
		t.Fatalf("the other members go on: %v", st)
	}
}
