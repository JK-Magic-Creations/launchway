package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const outputGate = `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.changed, equals: true}}
  done: {image: x, run: done, needs: fix, when: {upstream: always}}
`

// A step with when: { output: ... } runs when the output matches, and is
// skipped, saying why, when it differs or was never written.
func TestOutputConditionRunsAndSkips(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outputs map[string]string
		runs    bool
		reason  string
	}{
		{"matches", map[string]string{"changed": "true"}, true, ""},
		{"differs", map[string]string{"changed": "false"}, false, `scan.changed was "false", not "true"`},
		{"never written", nil, false, "scan.changed was not set"},
		{"another key written", map[string]string{"other": "true"}, false, "scan.changed was not set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEngine(t)
			id := create(t, e, outputGate, Trigger{Event: "manual"})
			ok(t, e, take(t, e), tc.outputs)
			if tc.runs {
				want(t, statuses(t, e, id), "fix", "queued")
				ok(t, e, take(t, e), nil)
			} else {
				want(t, statuses(t, e, id), "fix", "skipped")
				s := stepOf(t, e, id, "fix")
				if s.SkipKind != "condition" || s.Error != tc.reason {
					t.Fatalf("skip %q: %q, want condition: %q", s.SkipKind, s.Error, tc.reason)
				}
			}
			ok(t, e, take(t, e), nil) // done
			want(t, statuses(t, e, id), "run", "succeeded")
		})
	}
}

// Several tests on one step all have to hold, and a list of values matches
// any of them.
func TestOutputConditionWithOtherKeysAndLists(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.level, equals: [low, medium], branch: main}}
`, Trigger{Event: "push", Branch: "main"})
	ok(t, e, take(t, e), map[string]string{"level": "medium"})
	want(t, statuses(t, e, id), "fix", "queued")
	ok(t, e, take(t, e), nil)
	// On another branch the same output is not enough.
	id2 := create(t, e, `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.level, equals: [low, medium], branch: main}}
`, Trigger{Event: "push", Branch: "dev"})
	ok(t, e, take(t, e), map[string]string{"level": "low"})
	want(t, statuses(t, e, id2), "fix", "skipped", "run", "succeeded")
}

// A re-run treats a step skipped by an output condition as reusable.
func TestOutputConditionSkipIsReusedByARerun(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  scan: {image: x, run: scan}
  fix: {image: x, run: fix, needs: scan, when: {output: scan.changed, equals: true}}
  last: {image: x, run: last, needs: fix, when: {upstream: always}}
`, Trigger{Event: "manual"})
	ok(t, e, take(t, e), map[string]string{"changed": "false"})
	fail(t, e, take(t, e)) // last
	want(t, statuses(t, e, id), "fix", "skipped", "last", "failed")
	r, err := e.Rerun(RerunRequest{Run: id, From: "last", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	got := statuses(t, e, r.ID)
	want(t, got, "scan", "succeeded", "fix", "skipped", "last", "queued")
	if s := stepOf(t, e, r.ID, "fix"); s.SkipKind != "condition" || s.Error != "scan.changed was \"false\", not \"true\"" {
		t.Fatalf("%+v", s)
	}
}

const tolerant = `
pipeline: demo
steps:
  lint: {image: x, run: lint, on_failure: continue}
  build: {image: x, run: build, needs: lint}
  report: {image: x, run: report, needs: lint, when: {upstream: failed}}
`

// A failed step with on_failure: continue leaves the run succeeded, its
// dependents run, and the step is shown as failed (allowed).
func TestAllowedFailureLeavesTheRunSucceeded(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, tolerant, Trigger{Event: "manual"})
	fail(t, e, take(t, e))
	s := stepOf(t, e, id, "lint")
	if s.Status != StepFailed || !s.AllowedFailure || s.OnFailure != "continue" {
		t.Fatalf("%+v", s)
	}
	// build runs as if lint had succeeded; report asked for a failure, and
	// runs like after any failure.
	j1, j2 := take(t, e), take(t, e)
	if got := j1.StepID + "," + j2.StepID; got != "build,report" {
		t.Fatalf("leased %s", got)
	}
	ok(t, e, j1, nil)
	ok(t, e, j2, nil)
	d, _ := e.GetRun(id)
	if d.Status != RunSucceeded {
		t.Fatalf("run %s", d.Status)
	}
	want(t, statuses(t, e, id), "lint", "failed", "build", "succeeded", "report", "succeeded")
}

// A failure that is not tolerated still fails the run, even next to one
// that is, and a tolerant step that is retried is allowed only after its
// last attempt.
func TestAllowedFailureCannotHideARealOne(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  lint: {image: x, run: lint, on_failure: continue, retry: 1}
  test: {image: x, run: test}
  build: {image: x, run: build, needs: [lint, test]}
`, Trigger{Event: "manual"})
	j1, j2 := take(t, e), take(t, e)
	if j1.StepID != "lint" {
		j1, j2 = j2, j1
	}
	fail(t, e, j1) // retried: not allowed yet
	if s := stepOf(t, e, id, "lint"); s.AllowedFailure || s.Status != StepQueuedS {
		t.Fatalf("%+v", s)
	}
	fail(t, e, j2) // test fails for good
	fail(t, e, take(t, e))
	s := stepOf(t, e, id, "lint")
	if !s.AllowedFailure || s.Attempt != 2 {
		t.Fatalf("%+v", s)
	}
	want(t, statuses(t, e, id), "run", "failed", "test", "failed", "build", "skipped")
	if b := stepOf(t, e, id, "build"); !strings.Contains(b.Error, "test failed") {
		t.Fatalf("build was skipped because of: %q", b.Error)
	}
}

// Resume does not run an allowed failure again; a re-run reuses it.
func TestAllowedFailureSurvivesResumeAndRerun(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  lint: {image: x, run: lint, on_failure: continue}
  test: {image: x, run: test}
  build: {image: x, run: build, needs: [lint, test]}
`, Trigger{Event: "manual"})
	j1, j2 := take(t, e), take(t, e)
	if j1.StepID != "lint" {
		j1, j2 = j2, j1
	}
	fail(t, e, j1)
	fail(t, e, j2)
	want(t, statuses(t, e, id), "run", "failed", "lint", "failed", "test", "failed", "build", "skipped")

	plan, err := e.PlanRerun(id, "test")
	if err != nil || len(plan.Reuse) != 1 || plan.Reuse[0] != "lint" {
		t.Fatalf("%+v %v", plan, err)
	}
	re, err := e.Rerun(RerunRequest{Run: id, From: "test", Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if s := stepOf(t, e, re.ID, "lint"); s.Status != StepFailed || !s.AllowedFailure || s.ReusedFrom != id {
		t.Fatalf("a re-run must reuse the allowed failure: %+v", s)
	}
	ok(t, e, take(t, e), nil) // test, in the re-run
	ok(t, e, take(t, e), nil) // build
	if d, _ := e.GetRun(re.ID); d.Status != RunSucceeded {
		t.Fatalf("re-run %s", d.Status)
	}

	if err := e.Resume(id, "ann"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "lint", "failed", "test", "queued")
	if s := stepOf(t, e, id, "lint"); !s.AllowedFailure {
		t.Fatalf("resume must not reset an allowed failure: %+v", s)
	}
	j := take(t, e)
	if j.StepID != "test" || j.RunID != id {
		t.Fatalf("resume ran %s of %s", j.StepID, j.RunID)
	}
	ok(t, e, j, nil)
	ok(t, e, take(t, e), nil)
	if d, _ := e.GetRun(id); d.Status != RunSucceeded {
		t.Fatalf("resumed run %s", d.Status)
	}
}

// A server that does not know schema 24 leaves runs that use its keys alone,
// and refuses to create them.
func TestOlderServerLeavesFlowControlRunsAlone(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	cur := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	id := create(t, cur, tolerant, Trigger{Event: "manual"})
	if sum, err := cur.Summary(id); err != nil || sum.MinSchema != schemaFlow {
		t.Fatalf("min_schema %d %v", sum.MinSchema, err)
	}
	gated := create(t, cur, outputGate, Trigger{Event: "manual"})
	if sum, err := cur.Summary(gated); err != nil || sum.MinSchema != schemaFlow {
		t.Fatalf("min_schema %d %v", sum.MinSchema, err)
	}
	cur.Close()

	old := mustOpenAs(t, addr, clk, schemaResources, &syncBuffer{})
	none(t, old) // neither run's steps are leased
	if d, err := old.GetRun(id); err != nil || !d.NeedsNewerServer {
		t.Fatalf("%+v %v", d, err)
	}
	if err := old.Cancel(id, "ann"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("an older server changed the run: %v", err)
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, tolerant), Trigger: Trigger{Event: "manual"},
		Source: Source{Kind: "dir", Path: "/tmp"}}); err == nil || !strings.Contains(err.Error(), "flow control") {
		t.Fatalf("%v", err)
	}
	old.Close()

	again := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	if j := take(t, again); j.StepID != "lint" && j.StepID != "scan" {
		t.Fatalf("leased %s", j.StepID)
	}
}
