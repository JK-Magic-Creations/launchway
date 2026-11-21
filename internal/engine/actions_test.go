package engine

import (
	"errors"
	"testing"
)

func actionOf(t *testing.T, e *Engine, id, name string) Action {
	t.Helper()
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range d.Actions {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no action %s in %+v", name, d.Actions)
	return Action{}
}

// What the server says a person may do with a run is true: an action
// reported available succeeds in that state, and one reported unavailable is
// refused with the very same reason, naming the action that works.
func TestRunActionsAreTruthful(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	if a := actionOf(t, e, id, "resume"); a.Available || a.Reason == "" {
		t.Fatalf("a running run cannot be resumed: %+v", a)
	}
	if a := actionOf(t, e, id, "cancel"); !a.Available {
		t.Fatalf("%+v", a)
	}
	// A deployment opens, and the run fails.
	deployToHealthy(t, e, id, "1.0.0", "c1")
	fail(t, e, take(t, e)) // smoke fails
	j := leaseDeploy(t, e, deployer)
	if err := e.Finish(j.LeaseID, Result{Outcome: "failed", Error: "revert conflict"}); err != nil {
		t.Fatal(err)
	}
	resume := actionOf(t, e, id, "resume")
	if resume.Available || resume.Instead != "rerun from deploy-prod" {
		t.Fatalf("resume must be unavailable and name what works: %+v", resume)
	}
	err := e.Resume(id, "ann")
	if !errors.Is(err, ErrConflict) || err.Error() != "conflict: "+resume.Reason {
		t.Fatalf("the refusal must be the reported reason:\n%v\n%s", err, resume.Reason)
	}
	if a := actionOf(t, e, id, "rerun"); !a.Available {
		t.Fatalf("%+v", a)
	}
	if _, err := e.Rerun(RerunRequest{Run: id, From: "deploy-prod", Actor: "ann"}); err != nil {
		t.Fatalf("the action offered instead must work: %v", err)
	}
}

// A failed run with no deployment can be resumed, and the action says so.
func TestResumeAvailableMeansItWorks(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 2)
	fail(t, e, take(t, e)) // build fails
	a := actionOf(t, e, id, "resume")
	if !a.Available || a.Who != "operator" {
		t.Fatalf("%+v", a)
	}
	if err := e.Resume(id, "ann"); err != nil {
		t.Fatalf("an available action must succeed: %v", err)
	}
}
