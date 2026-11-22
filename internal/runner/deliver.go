package runner

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
)

// deliver sends a finished step's result, after any lines still waiting for
// the server, and keeps it for later when the server cannot be reached.
func (r *Runner) deliver(ctx context.Context, log *slog.Logger, sp *stepSpool, lease string, res engine.Result) {
	if !sp.Done() {
		// Lines are still waiting for the server: the result goes after them.
		r.keepResult(log, sp, res)
		return
	}
	for i := 0; i < 5; i++ {
		err := r.reportResult(ctx, log, lease, &res)
		if err == nil || errors.Is(err, ErrLeaseLost) {
			r.finished(log, res)
			return
		}
		if isOutage(err) {
			// The step's outcome is kept and delivered when the server
			// answers; it is not given up after a few tries.
			r.keepResult(log, sp, res)
			return
		}
		log.Warn("reporting the result failed; retrying", "err", err)
		time.Sleep(time.Duration(i+1) * time.Second)
	}
}

// releaseRun has the executor give back what it holds for the run, if it can.
func (r *Runner) releaseRun(parent context.Context, log *slog.Logger, runID string) {
	rel, ok := r.Exec.(executor.Releaser)
	if !ok {
		return
	}
	// The release must finish even when the runner is shutting down.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 30*time.Second)
	defer cancel()
	if err := rel.ReleaseRun(ctx, runID); err != nil {
		log.Warn("could not release what the lost step held; the reaper will", "err", err)
	}
}
