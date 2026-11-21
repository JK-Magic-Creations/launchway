package engine

import (
	"testing"
	"time"
)

// quiet stops the background reaper, so that a test drives the clock and the
// ticks itself.
func (e *Engine) quietReaper() {
	e.cancelBg()
	e.bg.Wait()
}

func leaseCount(t *testing.T, e *Engine) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM leases`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// While the database cannot be used (a failover) nobody can renew leases.
// When it is back, the first tick applies the restart rule: a running step
// keeps its lease for a fresh TTL instead of being expired and requeued at
// once. The runner then renews it.
func TestLeasesSurviveADatabaseOutage(t *testing.T) {
	e, clk := newEngine(t)
	e.quietReaper()
	real := time.Now()
	e.db.clk.real = func() time.Time { return real }
	run, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, fanIn), Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	r := RunnerInfo{ID: "r1", Name: "r1"}
	job := mustLease(t, e, r)
	e.markAlive() // the last good tick before the outage

	real = real.Add(3 * time.Minute) // the outage: nothing could be written for three minutes
	clk.Add(3 * time.Minute)
	e.markAlive() // the first tick after it
	e.reap()

	if leaseCount(t, e) == 0 {
		t.Fatal("the running step's lease was reaped right after the outage")
	}
	if _, err := e.Heartbeat(job.LeaseID); err != nil {
		t.Fatalf("the runner could not renew its lease after the outage: %v", err)
	}
	d, _ := e.GetRun(run.ID)
	for _, s := range d.StepList {
		if s.ID == job.StepID && s.Status != "running" {
			t.Fatalf("step %s is %s", s.ID, s.Status)
		}
	}
}

// A lease whose runner really went silent is still reaped: the rule applies
// only when this server could not write, not whenever the clock moves.
func TestSilentRunnerStillLosesItsLease(t *testing.T) {
	e, clk := newEngine(t)
	e.quietReaper()
	real := time.Now()
	e.db.clk.real = func() time.Time { return real }
	if _, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, fanIn), Source: Source{Kind: "dir", Path: "/tmp"}}); err != nil {
		t.Fatal(err)
	}
	mustLease(t, e, RunnerInfo{ID: "r1", Name: "r1"})
	e.markAlive()
	real = real.Add(time.Second) // this server kept writing
	clk.Add(10 * time.Minute)    // the runner heard nothing
	e.markAlive()
	e.reap()
	if n := leaseCount(t, e); n != 0 {
		t.Fatalf("%d leases left; a silent runner's lease must expire", n)
	}
}

// An injected clock is never adjusted; the real one follows the database's.
func TestClockFollowsTheDatabaseOnlyWhenNotInjected(t *testing.T) {
	c := &dbClock{}
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := c.wrap(func() time.Time { return fixed })(); !got.Equal(fixed) {
		t.Fatalf("an injected clock changed: %v", got)
	}
	c.skew.Store(int64(10 * time.Second))
	if got := time.Until(c.wrap(nil)()); got < 9*time.Second || got > 11*time.Second {
		t.Fatalf("the clock is %v ahead of local, want about 10s", got)
	}
}
