package engine

import (
	"context"
	"sync"
	"testing"
	"time"
)

var (
	r1 = RunnerInfo{ID: "r1", Name: "one"}
	r2 = RunnerInfo{ID: "r2", Name: "two"}
	r3 = RunnerInfo{ID: "r3", Name: "three"}
)

// leaseAs asks for a step for r without waiting, and starts it if there is
// one.
func leaseAs(t *testing.T, e *Engine, r RunnerInfo) *Job {
	t.Helper()
	j, err := e.Lease(context.Background(), r, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		if _, err := e.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
	}
	return j
}

func mustLease(t *testing.T, e *Engine, r RunnerInfo) *Job {
	t.Helper()
	j := leaseAs(t, e, r)
	if j == nil {
		t.Fatalf("%s got no step", r.ID)
	}
	return j
}

func noLease(t *testing.T, e *Engine, r RunnerInfo) {
	t.Helper()
	if j := leaseAs(t, e, r); j != nil {
		t.Fatalf("%s got %s/%s, want nothing", r.ID, j.RunID, j.StepID)
	}
}

const fanOut = `
pipeline: demo
steps:
  a: {image: x, run: a}
  b: {image: x, run: b, needs: [a]}
  c: {image: x, run: c, needs: [a]}
`

// Once a runner has taken a step of a run, the run's other steps go to it
// and to no other runner, all in the same workspace.
func TestRunStaysOnItsRunner(t *testing.T) {
	e, _ := newEngine(t)
	run := create(t, e, fanOut, Trigger{})
	a := mustLease(t, e, r1)
	ok(t, e, a, nil)
	noLease(t, e, r2)
	b, c := mustLease(t, e, r1), mustLease(t, e, r1)
	if b.RunID != run || c.RunID != run || b.StepID == c.StepID {
		t.Fatalf("got %s/%s and %s/%s", b.RunID, b.StepID, c.RunID, c.StepID)
	}
	for _, j := range []*Job{b, c} {
		if j.Workspace != a.Workspace || j.Workspace == "" || j.TakenOverFrom != "" {
			t.Fatalf("step %s: workspace %q taken over from %q; the first step had %q", j.StepID, j.Workspace, j.TakenOverFrom, a.Workspace)
		}
	}
	ok(t, e, b, nil)
	ok(t, e, c, nil)
	want(t, statuses(t, e, run), "run", "succeeded")
}

// A runner that is already working leaves a new run to a runner doing
// nothing; with no such runner, it takes the new run itself.
func TestDifferentRunsSpread(t *testing.T) {
	e, _ := newEngine(t)
	const one = "pipeline: demo\nsteps:\n  a: {image: x, run: a}\n"
	noLease(t, e, r2) // r2 is waiting for work
	run1 := create(t, e, one, Trigger{})
	run2 := create(t, e, one, Trigger{})
	if j := mustLease(t, e, r1); j.RunID != run1 {
		t.Fatalf("r1 got run %s, want the oldest, %s", j.RunID, run1)
	}
	noLease(t, e, r1) // r2 is idle, so run2 is left to it
	if j := mustLease(t, e, r2); j.RunID != run2 {
		t.Fatalf("r2 got run %s, want %s", j.RunID, run2)
	}
	run3 := create(t, e, one, Trigger{})
	if j := mustLease(t, e, r1); j.RunID != run3 {
		t.Fatalf("with every runner busy r1 got run %s, want %s", j.RunID, run3)
	}
}

// A run moves to another runner only once its runner has not been seen for
// RunnerTimeout, and then continues in a fresh workspace. The old runner
// does not get it back while the new one is alive.
func TestTakeoverAfterRunnerGoesQuiet(t *testing.T) {
	e, clk := newEngine(t)
	run := create(t, e, fanOut, Trigger{})
	a := mustLease(t, e, r1)
	ok(t, e, a, nil)

	clk.Add(e.runnerTimeout - time.Second)
	noLease(t, e, r2)
	clk.Add(2 * time.Second)
	b := mustLease(t, e, r2)
	if b.RunID != run || b.TakenOverFrom != r1.ID || b.Workspace == a.Workspace {
		t.Fatalf("takeover: run %s, from %q, workspace %q (was %q)", b.RunID, b.TakenOverFrom, b.Workspace, a.Workspace)
	}
	noLease(t, e, r1) // back, but r2 holds the run now
	c := mustLease(t, e, r2)
	if c.TakenOverFrom != "" || c.Workspace != b.Workspace {
		t.Fatalf("the next step on r2: from %q, workspace %q, want %q", c.TakenOverFrom, c.Workspace, b.Workspace)
	}
	ok(t, e, b, nil)
	ok(t, e, c, nil)
	want(t, statuses(t, e, run), "run", "succeeded")
}

// A runner whose every slot is busy calls Lease only when a step ends.
// Its heartbeats and log uploads alone keep its runs from being taken over.
func TestBusyRunnerKeepsItsRuns(t *testing.T) {
	e, clk := newEngine(t)
	run := create(t, e, "pipeline: demo\nsteps:\n  a: {image: x, run: a}\n  b: {image: x, run: b}\n", Trigger{})
	a := mustLease(t, e, r1) // b stays queued for r1, which has no free slot
	for i := 0; i < 20; i++ {
		clk.Add(20 * time.Second)
		var err error
		if i%2 == 0 {
			_, err = e.Heartbeat(a.LeaseID)
		} else {
			err = e.AppendLogs(a.LeaseID, []LogLine{{Text: "still going"}})
		}
		if err != nil {
			t.Fatal(err)
		}
		noLease(t, e, r2)
	}
	ok(t, e, a, nil)
	if b := mustLease(t, e, r1); b.RunID != run || b.Workspace != a.Workspace {
		t.Fatalf("got %s/%s in workspace %q, want run %s in %q", b.RunID, b.StepID, b.Workspace, run, a.Workspace)
	}
}

// A runner that says it is leaving lets others take its runs at once.
func TestRunnerLeaving(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, fanOut, Trigger{})
	ok(t, e, mustLease(t, e, r1), nil)
	noLease(t, e, r2)
	if err := e.RunnerLeft(r1.ID); err != nil {
		t.Fatal(err)
	}
	if j := mustLease(t, e, r2); j.TakenOverFrom != r1.ID {
		t.Fatalf("taken over from %q", j.TakenOverFrom)
	}
}

// With two servers, runners on either agree on who holds a run: a busy
// runner heartbeating through either keeps it, and when two runners on
// different servers try to take it over at once, exactly one does.
func TestAffinityAcrossServers(t *testing.T) {
	a, b, clk := pair(t)
	run := create(t, a, fanOut, Trigger{})
	ok(t, b, mustLease(t, a, r1), nil)
	noLease(t, b, r2)
	noLease(t, a, r3)
	held := mustLease(t, b, r1) // c stays queued for r1

	for i := 0; i < 5; i++ {
		clk.Add(20 * time.Second)
		if _, err := []*Engine{a, b}[i%2].Heartbeat(held.LeaseID); err != nil {
			t.Fatal(err)
		}
		for _, srv := range []*Engine{a, b} {
			noLease(t, srv, r2)
			noLease(t, srv, r3)
		}
	}

	// r1 goes quiet: its step is requeued, and r2 and r3 race for the run.
	clk.Add(a.runnerTimeout + time.Second)
	a.reap()
	var wg sync.WaitGroup
	jobs := make([]*Job, 2)
	for i, srv := range []*Engine{a, b} {
		wg.Add(1)
		go func(i int, srv *Engine, r RunnerInfo) {
			defer wg.Done()
			j, err := srv.Lease(context.Background(), r, 0)
			if err != nil {
				t.Error(err)
			}
			jobs[i] = j
		}(i, srv, []RunnerInfo{r2, r3}[i])
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	var winner RunnerInfo
	var first *Job
	switch {
	case jobs[0] != nil && jobs[1] == nil:
		winner, first = r2, jobs[0]
	case jobs[1] != nil && jobs[0] == nil:
		winner, first = r3, jobs[1]
	default:
		t.Fatalf("jobs %+v, %+v: want exactly one runner to take over", jobs[0], jobs[1])
	}
	if first.TakenOverFrom != r1.ID {
		t.Fatalf("taken over from %q", first.TakenOverFrom)
	}
	if err := b.Finish(first.LeaseID, Result{Outcome: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	// The run's last step goes to the winner, through either server.
	for _, r := range []RunnerInfo{r1, r2, r3} {
		if r != winner {
			noLease(t, a, r)
			noLease(t, b, r)
		}
	}
	last := mustLease(t, b, winner)
	if last.Workspace != first.Workspace {
		t.Fatalf("workspace %q, want %q", last.Workspace, first.Workspace)
	}
	ok(t, a, last, nil)
	want(t, statuses(t, a, run), "run", "succeeded")
}

// Runners not seen for a day are forgotten; ones not seen for two minutes
// are listed as offline.
func TestRunnerList(t *testing.T) {
	e, clk := newEngine(t)
	noLease(t, e, r1)
	clk.Add(25 * time.Hour)
	noLease(t, e, r2)
	e.forgetRunners()
	list, err := e.Runners()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != r2.ID || !list[0].Online {
		t.Fatalf("runners %+v, want only r2, online", list)
	}
	clk.Add(RunnerOffline + time.Second)
	if list, _ = e.Runners(); len(list) != 1 || list[0].Online {
		t.Fatalf("runners %+v, want r2 offline", list)
	}
}
