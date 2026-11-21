package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func inspectTarget() InspectTarget {
	return InspectTarget{RunID: "run_x", Deployment: "dep_x", Project: "p", Target: "kind"}
}

// Limits: open requests per Airfield, followed logs per person and per
// Airfield; an unanswered request ends; a followed log nobody watches is
// told to stop.
func TestInspectLimitsAndIdle(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	tg := inspectTarget()
	for i := 0; i < MaxStreamsPerUser; i++ {
		if _, err := e.EnqueueInspect(InspectRequest{Target: tg, Kind: InspectLogs, Actor: "ada", Follow: true}); err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
	}
	if _, err := e.EnqueueInspect(InspectRequest{Target: tg, Kind: InspectLogs, Actor: "ada", Follow: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a fifth stream for one person: %v", err)
	}
	if _, err := e.EnqueueInspect(InspectRequest{Target: tg, Kind: InspectLogs, Actor: "bob", Follow: true}); err != nil {
		t.Fatalf("another person's stream: %v", err)
	}
	for i := 0; ; i++ {
		_, err := e.EnqueueInspect(InspectRequest{Target: tg, Kind: InspectTree, Actor: "x"})
		if err != nil {
			if !errors.Is(err, ErrConflict) || i > MaxOpenInspects {
				t.Fatalf("after %d: %v", i, err)
			}
			break
		}
	}
	// Nobody took them: after two minutes they end.
	clk.Add(3 * time.Minute)
	e.purgeInspect(e.now().UnixMilli() + 1_000_000)
	if n, _ := e.OpenInspects("kind"); n != 0 {
		t.Fatalf("%d requests still open after two minutes unanswered", n)
	}
}

func TestInspectIdleFollowIsToldToStop(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	e := mustOpenAs(t, addr, clk, 0, &syncBuffer{})
	tg := inspectTarget()
	id, err := e.EnqueueInspect(InspectRequest{Target: tg, Kind: InspectLogs, Actor: "ada", Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	r := RunnerInfo{ID: "r", Name: "r", Targets: []string{"kind"}}
	// The take reads the run, which does not exist here: it ends the request.
	if j, _ := e.TakeInspect(context.Background(), r, r.Targets, 0); j != nil {
		t.Fatalf("took %+v", j)
	}
	out, err := e.AwaitInspect(context.Background(), id, time.Second)
	if err != nil || out.State != InspectFailed {
		t.Fatalf("%+v %v", out, err)
	}
	// Chunks for a request that is not taken tell the runner to stop.
	if stop, _ := e.InspectChunk(r, r.Targets, id, "[]"); !stop {
		t.Fatal("a finished request keeps a runner reading")
	}
	if _, err := e.InspectChunk(r, []string{"other"}, id, "[]"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another Airfield's runner: %v", err)
	}
}
