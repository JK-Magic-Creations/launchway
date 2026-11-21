package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// two independent branches, each of two steps, joined by a last step.
const twoBranches = `
pipeline: demo
steps:
  setup: {image: x, run: s}
  a1: {image: x, run: a1, needs: setup}
  a2: {image: x, run: a2, needs: a1}
  b1: {image: x, run: b1, needs: setup}
  b2: {image: x, run: b2, needs: b1}
  join: {image: x, run: j, needs: [a2, b2]}
`

// failBoth runs the pipeline until both branches fail at their first step.
func failBoth(t *testing.T, e *Engine) string {
	t.Helper()
	id := create(t, e, twoBranches, Trigger{Event: "manual"})
	ok(t, e, take(t, e), map[string]string{"v": "1"}) // setup
	j1, j2 := take(t, e), take(t, e)
	fail(t, e, j1)
	fail(t, e, j2)
	want(t, statuses(t, e, id), "run", "failed", "a1", "failed", "b1", "failed", "a2", "skipped", "b2", "skipped", "join", "skipped")
	return id
}

// A run with two failed parallel branches is re-run in one new run with
// failed: true, reusing everything else, and the plan says what runs.
func TestRerunFailedCoversTwoBranches(t *testing.T) {
	e, _ := newEngine(t)
	id := failBoth(t, e)
	plan, err := e.PlanRerunFrom(id, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.From, []string{"a1", "b1"}) ||
		!reflect.DeepEqual(plan.Run, []string{"a1", "a2", "b1", "b2", "join"}) || !reflect.DeepEqual(plan.Reuse, []string{"setup"}) {
		t.Fatalf("%+v", plan)
	}
	re, err := e.Rerun(RerunRequest{Run: id, Failed: true, Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if re.RerunFrom != "a1" || !reflect.DeepEqual(re.RerunFroms, []string{"a1", "b1"}) || re.MinSchema != schemaFlow {
		t.Fatalf("%+v", re)
	}
	want(t, statuses(t, e, re.ID), "setup", "succeeded", "a1", "queued", "b1", "queued", "a2", "pending")
	if s := stepOf(t, e, re.ID, "setup"); s.Outputs["v"] != "1" || s.ReusedFrom != id {
		t.Fatalf("outputs are kept: %+v", s)
	}
	d, _ := e.GetRun(id)
	if len(d.Reruns) != 1 || !reflect.DeepEqual(d.Reruns[0].Froms, []string{"a1", "b1"}) {
		t.Fatalf("%+v", d.Reruns)
	}
	for range 5 {
		ok(t, e, take(t, e), nil)
	}
	want(t, statuses(t, e, re.ID), "run", "succeeded")
}

// Several steps named by hand: the union of what comes after each; the rules
// for what is reused apply to the rest as before.
func TestRerunFromSeveralSteps(t *testing.T) {
	e, _ := newEngine(t)
	id := failBoth(t, e)
	plan, err := e.PlanRerunFrom(id, []string{"a1", "b1"}, false)
	if err != nil || len(plan.Run) != 5 {
		t.Fatalf("%+v %v", plan, err)
	}
	// Overlapping unions: a1 and a2 both start from the first branch.
	if plan, err = e.PlanRerunFrom(id, []string{"a1", "a2", "b1"}, false); err != nil || len(plan.Run) != 5 {
		t.Fatalf("%+v %v", plan, err)
	}
	// One failed branch outside the union still refuses, as before.
	_, err = e.PlanRerunFrom(id, []string{"a1"}, false)
	if err == nil || !strings.Contains(err.Error(), "b1 (failed)") || !strings.Contains(err.Error(), "does not come after a1") {
		t.Fatalf("%v", err)
	}
	// Naming the same step twice, in two ways, is one step.
	re, err := e.Rerun(RerunRequest{Run: id, From: "a1", Froms: []string{"a1", "b1"}, Actor: "ann"})
	if err != nil || !reflect.DeepEqual(re.RerunFroms, []string{"a1", "b1"}) {
		t.Fatalf("%+v %v", re, err)
	}
	if _, err := e.PlanRerunFrom(id, []string{"a1", "nope"}, false); err == nil || !strings.Contains(err.Error(), "no step nope") {
		t.Fatalf("%v", err)
	}
	if _, err := e.PlanRerunFrom(id, nil, false); err == nil {
		t.Fatal("no step named")
	}
}

// A re-run from one step is exactly what it was: no list, no newer schema.
func TestRerunFromOneStepIsUnchanged(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, linear, Trigger{Event: "manual"})
	ok(t, e, take(t, e), nil)
	fail(t, e, take(t, e))
	re, err := e.Rerun(RerunRequest{Run: id, From: "b", Actor: "ann"})
	if err != nil || re.RerunFrom != "b" || re.RerunFroms != nil || re.MinSchema >= schemaFlow {
		t.Fatalf("%+v %v", re, err)
	}
	// failed: true with one failed branch is a single start too.
	e, _ = newEngine(t)
	id2 := create(t, e, linear, Trigger{Event: "manual"})
	ok(t, e, take(t, e), nil)
	ok(t, e, take(t, e), nil)
	fail(t, e, take(t, e))
	re, err = e.Rerun(RerunRequest{Run: id2, Failed: true, Actor: "ann"})
	if err != nil || re.RerunFrom != "c" || re.RerunFroms != nil || re.MinSchema >= schemaFlow {
		t.Fatalf("%+v %v", re, err)
	}
	// Nothing failed: nothing to run again.
	e, _ = newEngine(t)
	id3 := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: a}\n", Trigger{Event: "manual"})
	ok(t, e, take(t, e), nil)
	if _, err := e.PlanRerunFrom(id3, nil, true); err == nil || !strings.Contains(err.Error(), "nothing to run again") {
		t.Fatalf("%v", err)
	}
}

// Canceled steps are covered too, and a failure the pipeline allows is not
// something to run again.
func TestRerunFailedCoversCanceledAndSkipsAllowedFailures(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, `
pipeline: demo
steps:
  lint: {image: x, run: l, on_failure: continue}
  a: {image: x, run: a}
  b: {image: x, run: b, needs: [lint, a]}
`, Trigger{Event: "manual"})
	j1, j2 := take(t, e), take(t, e)
	if j1.StepID != "lint" {
		j1, j2 = j2, j1
	}
	fail(t, e, j1)
	if err := e.Cancel(id, "ann"); err != nil {
		t.Fatal(err)
	}
	if err := e.Finish(j2.LeaseID, Result{Outcome: "canceled"}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "canceled", "lint", "failed", "a", "canceled", "b", "canceled")
	plan, err := e.PlanRerunFrom(id, nil, true)
	if err != nil || !reflect.DeepEqual(plan.From, []string{"a"}) || !reflect.DeepEqual(plan.Reuse, []string{"lint"}) {
		t.Fatalf("%+v %v", plan, err)
	}
}

// A deploy step reused would make the plan wrong, as it always did: a
// failed step in the union must not leave a deploy step to be reused.
func TestRerunFailedRefusesToReuseADeployStep(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), healthy(false))
	// Whatever the run's state, planning never plans to reuse a deploy step.
	if plan, err := e.PlanRerunFrom(id, []string{"build"}, false); err == nil {
		for _, r := range plan.Reuse {
			if r == "deploy-prod" {
				t.Fatalf("a deploy step would be reused: %+v", plan)
			}
		}
	}
}

// An older server leaves a run re-run from several steps alone: it would
// have read only the first.
func TestOlderServerLeavesAMultiStepRerunAlone(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	cur := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	id := failBoth(t, cur)
	re, err := cur.Rerun(RerunRequest{Run: id, Failed: true, Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	cur.Close()
	old := mustOpenAs(t, addr, clk, schemaResources, &syncBuffer{})
	before := countEvents(t, old, re.ID, StepQueued, RunCancelRequested, StepSkipped)
	none(t, old)
	if err := old.Cancel(re.ID, "ann"); err == nil {
		t.Fatal("an older server changed a run it does not understand")
	}
	if got := countEvents(t, old, re.ID, StepQueued, RunCancelRequested, StepSkipped); got != before {
		t.Fatalf("events changed: %d -> %d", before, got)
	}
	if _, err := old.Rerun(RerunRequest{Run: re.ID, From: "a1"}); err == nil {
		t.Fatal("an older server re-ran a run it does not understand")
	}
}
