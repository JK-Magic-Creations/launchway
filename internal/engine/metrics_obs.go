package engine

import (
	"sync"

	"github.com/jk-magic-creations/launchway/internal/metrics"
)

// What a command changed in a run is counted in the metrics once it is
// committed: the run's and each step's status before and after are compared,
// so every path that moves a run (a lease, a finish, a reaper, a cancel) is
// counted in one place and none can be forgotten. Nothing here reads or keeps
// a repository, a branch, a person or a run id.

type runSnapshot struct {
	status RunStatus
	steps  map[string]stepSnap
}

type stepSnap struct {
	status   StepStatus
	attempt  int
	queuedAt int64
}

func snapshotRun(st *RunState) *runSnapshot {
	if st == nil {
		return nil
	}
	s := &runSnapshot{status: st.Status, steps: make(map[string]stepSnap, len(st.Steps))}
	for id, x := range st.Steps {
		s.steps[id] = stepSnap{status: x.Status, attempt: x.Attempt, queuedAt: x.QueuedAt}
	}
	return s
}

// runnerPools remembers each Station's pool, for the queue wait metric.
var runnerPools sync.Map // runner id -> pool name

func poolName(runner string) string {
	if p, ok := runnerPools.Load(runner); ok && p.(string) != "" {
		return p.(string)
	}
	return DefaultPool
}

// observe counts what changed between before and the state now.
func observeTransitions(before *runSnapshot, st *RunState, now int64) {
	if st == nil {
		return
	}
	traceTransitions(before, st, now)
	prevRun := RunStatus("")
	if before != nil {
		prevRun = before.status
	}
	if st.Status != prevRun {
		switch st.Status {
		case RunRunning:
			if prevRun == "" || prevRun == RunQueued {
				metrics.RunsStarted.Inc()
			}
		case RunSucceeded, RunFailed, RunCanceled:
			if prevRun != RunSucceeded && prevRun != RunFailed && prevRun != RunCanceled {
				metrics.RunsFinished.Inc(string(st.Status))
				if st.StartedAt > 0 && st.FinishedAt >= st.StartedAt {
					metrics.RunDuration.Observe(float64(st.FinishedAt-st.StartedAt)/1000, string(st.Status))
				}
				if metrics.NameLabels() {
					metrics.FlightRuns.Inc(st.Project, string(st.Status))
				}
			}
		}
	}
	if before == nil {
		return
	}
	observeSteps(before, st, now)
}

// observeSteps counts what changed in each step.
func observeSteps(before *runSnapshot, st *RunState, now int64) {
	for id, s := range st.Steps {
		b, known := before.steps[id]
		if !known {
			continue
		}
		if s.Status == b.status && s.Attempt == b.attempt {
			continue
		}
		if b.status == StepQueuedS && (s.Status == StepLeasedS || s.Status == StepRunning) && b.queuedAt > 0 && s.Runner != "" {
			wait := float64(now-b.queuedAt) / 1000
			if wait < 0 {
				wait = 0
			}
			metrics.QueueWait.Observe(wait, poolName(s.Runner))
		}
		if (b.status != StepRunning && s.Status == StepRunning) || (b.status == StepQueuedS && s.Status == StepLeasedS) {
			metrics.StepsStarted.Inc()
		}
		if s.Status.Terminal() && !b.status.Terminal() {
			out := string(s.Status)
			metrics.StepsFinished.Inc(out)
			if s.StartedAt > 0 && s.FinishedAt >= s.StartedAt {
				metrics.StepDuration.Observe(float64(s.FinishedAt-s.StartedAt)/1000, out)
			}
		}
	}
}

// migrateCounted applies the next migration and counts it in the metrics.
func migrateCounted(d *dbx, known int) (int, error) {
	n, err := migrateOne(d, known)
	if err == nil && n > 0 {
		metrics.MigrationsApplied.Inc()
	}
	return n, err
}
