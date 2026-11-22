package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

func TestBeatEvery(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  engine.Job
		want time.Duration
	}{
		{"the server's beat", engine.Job{LeaseTTL: pipeline.Duration(90 * time.Second), Beat: pipeline.Duration(15 * time.Second)}, 15 * time.Second},
		{"an older server: a third of the lease", engine.Job{LeaseTTL: pipeline.Duration(30 * time.Second)}, 10 * time.Second},
		{"never faster than a second", engine.Job{LeaseTTL: pipeline.Duration(time.Second)}, time.Second},
	} {
		if got := beatEvery(&tc.job); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	// A call may take at least minCall to answer, whatever the beat.
	if callWithin(time.Second) != minCall || callWithin(time.Minute) != time.Minute {
		t.Error("callWithin")
	}
}

// scripted answers heartbeats from a list.
type scripted struct {
	mu    sync.Mutex
	steps []func(ctx context.Context) (bool, error)
	n     int
	times []time.Time
}

func (s *scripted) Heartbeat(ctx context.Context, _ string) (bool, error) {
	s.mu.Lock()
	i := s.n
	s.n++
	s.times = append(s.times, time.Now())
	var f func(context.Context) (bool, error)
	if i < len(s.steps) {
		f = s.steps[i]
	}
	s.mu.Unlock()
	if f == nil {
		return false, nil
	}
	return f(ctx)
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A heartbeat that hangs (a server busy for longer than a beat), or fails, is
// not the end of the lease: the next one comes soon after and the step is
// never reported lost. Only the server saying the lease is gone stops it.
func TestKeepLeaseSurvivesSlowAndFailedHeartbeats(t *testing.T) {
	defer func(a, b time.Duration) { retryAfter, minCall = a, b }(retryAfter, minCall)
	retryAfter, minCall = 10*time.Millisecond, 50*time.Millisecond
	c := &scripted{steps: []func(context.Context) (bool, error){
		func(ctx context.Context) (bool, error) { <-ctx.Done(); return false, ctx.Err() }, // hangs until its timeout
		func(context.Context) (bool, error) { return false, errors.New("connection reset") },
		func(context.Context) (bool, error) { return false, nil }, // succeeds
		func(context.Context) (bool, error) { return true, nil },  // asks to cancel
		func(context.Context) (bool, error) { return false, ErrLeaseLost },
	}}
	// A beat is never under a second; what is shortened is the call and the retry.
	job := &engine.Job{LeaseID: "l1", Beat: pipeline.Duration(minBeat)}
	type ev struct{ canceled, lost bool }
	events := make(chan ev, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		keepLease(context.Background(), make(chan struct{}), c, job, quietLog(), func(cn, l bool) { events <- ev{cn, l} })
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("keepLease did not end after the lease was lost")
	}
	close(events)
	var got []ev
	for e := range events {
		got = append(got, e)
	}
	if len(got) != 2 || !got[0].canceled || !got[1].lost {
		t.Fatalf("events %+v: want a cancel request, then the loss, and no loss before", got)
	}
	// The two failures were retried within retryAfter, not a whole beat later.
	if gap := c.times[2].Sub(c.times[1]); gap > 500*time.Millisecond {
		t.Errorf("a failed heartbeat was retried after %v", gap)
	}
}

func TestKeepLeaseStops(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		keepLease(context.Background(), stop, &scripted{}, &engine.Job{Beat: pipeline.Duration(time.Hour)}, quietLog(), func(bool, bool) { t.Error("called") })
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
}
