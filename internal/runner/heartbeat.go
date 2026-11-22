package runner

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Heartbeats keep a step's lease. The server gives a lease a lifetime (the
// lease_ttl it tells the runner, 90 s by default) and takes the step back
// when none has come for that long. What the runner does about a slow or
// missing answer:
//
//   - a heartbeat goes every beat (the server's Beat, else a third of the
//     lease_ttl), in a goroutine of its own, never behind log uploads;
//   - one call waits at most callWithin(beat) for an answer, so that a hung
//     connection costs one beat, not the lease;
//   - a heartbeat that failed is tried again after retryAfter, not after a
//     whole beat, so a single slow or lost answer costs nothing: with the
//     defaults (beat 15 s, lease 90 s) five beats in a row have to fail
//     before the server takes the step back.
const minBeat = time.Second

// retryAfter and minCall are variables only so that tests can shorten them.
var (
	retryAfter = 2 * time.Second
	minCall    = 10 * time.Second
)

// beatEvery is how often a step's lease is renewed.
func beatEvery(job *engine.Job) time.Duration {
	if b := job.Beat.D(); b > 0 {
		return max(b, minBeat)
	}
	return max(job.LeaseTTL.D()/3, minBeat)
}

// callWithin is how long one heartbeat call waits: as long as a beat, but
// never less than ten seconds, so a server that takes 15 s to answer still
// gets a heartbeat through when the lease is long enough to allow it.
func callWithin(beat time.Duration) time.Duration { return max(beat, minCall) }

type beater interface {
	Heartbeat(ctx context.Context, lease string) (bool, error)
}

// keepLease sends heartbeats until stop is closed. It calls on with
// (canceled, lost) when the server asks to cancel the run or no longer has
// the lease; after a loss it stops.
func keepLease(ctx context.Context, stop <-chan struct{}, c beater, job *engine.Job, log *slog.Logger, on func(canceled, lost bool)) {
	beat := beatEvery(job)
	next := beat
	for {
		t := time.NewTimer(next)
		select {
		case <-stop:
			t.Stop()
			return
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, callWithin(beat))
		canceled, err := c.Heartbeat(cctx, job.LeaseID)
		cancel()
		switch {
		case errors.Is(err, ErrLeaseLost):
			on(false, true)
			return
		case err != nil:
			log.Warn("heartbeat failed; trying again soon", "err", err)
			next = min(retryAfter, beat)
		default:
			next = beat
			if canceled {
				on(true, false)
			}
		}
	}
}

// flushEvery flushes the step's log lines every logFlushEvery until stop is closed.
func flushEvery(stop <-chan struct{}, flush func()) {
	fl := time.NewTicker(logFlushEvery)
	defer fl.Stop()
	for {
		select {
		case <-stop:
			return
		case <-fl.C:
			flush()
		}
	}
}

// leaseEvents says what to do when the server cancels the run or takes the
// step's lease away.
func leaseEvents(log *slog.Logger, sys func(string, ...any), stopWith func(requested, lost bool)) func(canceled, lost bool) {
	return func(canceled, lost bool) {
		if lost {
			log.Warn("lease lost; stopping the step")
			stopWith(false, true)
		} else if canceled {
			sys("The run was canceled; stopping.")
			stopWith(true, false)
		}
	}
}
