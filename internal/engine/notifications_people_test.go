package engine

import (
	"context"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/notify"
)

// People are told of what the notification service notices even when no
// channel rule names it, once for each event, and the people of a run can be
// asked for.
func TestPersonNotifierSeesEventsWithoutAnyRule(t *testing.T) {
	e, n := notifyEngine(t, notify.Config{})
	var mu sync.Mutex
	var got []notify.Event
	e.SetPersonNotifier(func(_ context.Context, ev notify.Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	runID := toApproval(t, e)
	e.notifyOnce(context.Background(), n)
	e.notifyOnce(context.Background(), n)
	mu.Lock()
	defer mu.Unlock()
	var approvals int
	for _, ev := range got {
		if ev.Kind == notify.ApprovalRequested && ev.RunID == runID && ev.Step == "approve" && ev.Key != "" {
			approvals++
		}
	}
	if approvals == 0 {
		t.Fatalf("the approval was not offered to the person notifier: %+v", got)
	}
	if log, err := e.Notifications(10, nil); err != nil || len(log) != 0 {
		t.Fatalf("with no rules nothing is queued for channels: %v %+v", err, log)
	}
	if _, approvers := e.RunPeople(context.Background(), runID); len(approvers) != 0 {
		t.Fatalf("nobody has approved yet: %v", approvers)
	}
	e.SetPersonNotifier(nil)
	if e.personHook() != nil {
		t.Fatal("the hook was not removed")
	}
}
