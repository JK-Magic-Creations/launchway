package runner

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/executor"
)

type reapStub struct {
	stubExec
	calls chan struct{}
}

func (s reapStub) Cleanup(context.Context, executor.RunLookup) error {
	s.calls <- struct{}{}
	return nil
}

// The reaper looks at once on start (a crashed runner's pods are found by its
// successor), and again soon after a step reports its result, which may be
// the run's last, without waiting for the interval.
func TestReaperRunsAtStartAndAfterAStep(t *testing.T) {
	st := reapStub{calls: make(chan struct{}, 8)}
	r := &Runner{Exec: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), reapKick: make(chan struct{}, 1), Client: &Client{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.reap(ctx, st, r.reapKick, time.Hour) // the interval never fires in this test
	wait := func(what string) {
		t.Helper()
		select {
		case <-st.calls:
		case <-time.After(10 * time.Second):
			t.Fatalf("no clean-up %s", what)
		}
	}
	wait("at start")
	r.reapSoon()
	r.reapSoon() // coalesced, never blocks
	wait("after a step finished")
}
