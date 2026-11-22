package engine

import (
	"context"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/notify"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// The notification service also tells people (internal/notices): every event
// it notices, for the channels' rules or not, is offered to the person
// notifier too, which works out whom it concerns. These are what it needs to
// know of a run.

// RunFixedEvent is the event of a run that succeeded after the one before it,
// on the same branch of the same Flight, failed. Only the person notifier
// sees it; channel rules cannot name it.
const RunFixedEvent = "run.fixed"

var personHooks sync.Map // *Engine -> func(context.Context, notify.Event)

// SetPersonNotifier gives the notification service the function that tells
// people of events. nil turns it off.
func (e *Engine) SetPersonNotifier(f func(context.Context, notify.Event)) {
	if f == nil {
		personHooks.Delete(e)
		return
	}
	personHooks.Store(e, f)
}

func (e *Engine) personHook() func(context.Context, notify.Event) {
	if f, ok := personHooks.Load(e); ok {
		return f.(func(context.Context, notify.Event))
	}
	return nil
}

// RunPeople says who started a run and who approved steps of it.
func (e *Engine) RunPeople(_ context.Context, runID string) (actor string, approvers []string) {
	return store.RunActor(e.db, runID), store.RunApprovers(e.db, runID)
}

// StepApprovers says who the named approvers of a waiting approval are (users
// only: a group has no list here).
func (e *Engine) StepApprovers(runID, step string) []string {
	ap, err := e.Approvals()
	if err != nil {
		return nil
	}
	for _, a := range ap {
		if a.RunID == runID && a.StepID == step && a.Approvers != nil {
			return append([]string(nil), a.Approvers.Users...)
		}
	}
	return nil
}

// priorRunFailed says whether the run before this one on the same branch of
// the same Flight had failed.
func (e *Engine) priorRunFailed(project, branch string, number int) bool {
	return store.PriorRunStatus(e.db, project, branch, number) == string(RunFailed)
}
