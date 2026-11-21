package engine

import (
	"strings"
	"testing"
)

// A deployment that ends is a launch result with where it went and what; a
// run that ends is a run result; the cursor starts at the newest event, so
// history starts nothing, and only moves forward.
func TestResultsOfRunsAndLaunches(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	cur, err := e.ResultCursor()
	if err != nil || cur != 0 {
		t.Fatalf("%d %v", cur, err)
	}
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1.4.2")
	j := leaseDeploy(t, e, deployer)
	applied(t, e, j, "c0ffee")
	it := watchOf(t, e, deployer, "deploy-prod")
	observe(t, e, deployer, it, healthy(true))
	ok(t, e, take(t, e), nil)

	results, err := e.ResultsAfter(cur, 50)
	if err != nil || len(results) != 2 {
		t.Fatalf("%v %+v", err, results)
	}
	launch, run := results[0], results[1]
	if launch.Kind != TriggerOnLaunch || launch.Verdict != VerdictDeployed || launch.Environment != "prod" || launch.Service != "shop" || launch.Version != "1.4.2" || launch.RunID != id {
		t.Fatalf("%+v", launch)
	}
	if run.Kind != TriggerOnRun || run.Verdict != "succeeded" || run.RunID != id {
		t.Fatalf("%+v", run)
	}
	e.MoveResultCursor(run.Seq)
	e.MoveResultCursor(1)
	if c, _ := e.ResultCursor(); c != run.Seq {
		t.Fatalf("the cursor is %d, want %d", c, run.Seq)
	}
	if after, _ := e.ResultsAfter(run.Seq, 50); len(after) != 0 {
		t.Fatalf("%+v", after)
	}
}

func TestRunTriggerRules(t *testing.T) {
	e, _ := newEngine(t)
	if err := e.AllowRunTriggers(RunTriggerAllow{Upstream: "a", Downstream: "a"}); err == nil {
		t.Fatal("a project allowed itself")
	}
	mk := func(down, up string) error {
		_, err := e.CreateRunTrigger(RunTrigger{Project: down, Name: "t", UpstreamProject: up, When: "any"})
		return err
	}
	if err := mk("b", "a"); err == nil || !strings.Contains(err.Error(), "has not allowed b") {
		t.Fatalf("%v", err)
	}
	for _, p := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "a"}} {
		if err := e.AllowRunTriggers(RunTriggerAllow{Upstream: p[0], Downstream: p[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mk("b", "a"); err != nil { // a -> b
		t.Fatal(err)
	}
	if err := mk("c", "b"); err != nil { // b -> c
		t.Fatal(err)
	}
	if err := mk("a", "c"); err == nil || !strings.Contains(err.Error(), "cycle") { // c -> a closes the ring
		t.Fatalf("%v", err)
	}
	if _, err := e.CreateRunTrigger(RunTrigger{Project: "b", Name: "x", UpstreamProject: "a", Kind: "launch", When: "succeeded"}); err == nil {
		t.Fatal("a launch trigger took a run's result")
	}
	if _, err := e.CreateRunTrigger(RunTrigger{Project: "b", Name: "t", UpstreamProject: "a", When: "any"}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("%v", err)
	}
	on, _ := e.RunTriggersOn("a")
	if len(on) != 1 || on[0].Project != "b" || !on[0].Enabled {
		t.Fatalf("%+v", on)
	}
}
