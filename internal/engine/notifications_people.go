package engine

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jk-magic-creations/launchway/internal/notify"
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
func (e *Engine) RunPeople(ctx context.Context, runID string) (actor string, approvers []string) {
	_ = e.db.QueryRow(`SELECT actor FROM runs WHERE id = ?`, runID).Scan(&actor)
	rows, err := e.db.Query(`SELECT data FROM events WHERE run_id = ? AND type IN ('step.approved', 'step.approval_recorded') ORDER BY seq LIMIT 50`, runID)
	if err != nil {
		return actor, nil
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var data string
		if rows.Scan(&data) != nil {
			continue
		}
		var p struct {
			Actor     string           `json:"actor"`
			Approvals []ApprovalRecord `json:"approvals"`
		}
		if json.Unmarshal([]byte(data), &p) != nil {
			continue
		}
		names := []string{p.Actor}
		for _, a := range p.Approvals {
			names = append(names, a.Actor)
		}
		for _, n := range names {
			if n != "" && !seen[n] {
				seen[n] = true
				approvers = append(approvers, n)
			}
		}
	}
	return actor, approvers
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
	var status string
	err := e.db.QueryRow(`SELECT status FROM runs WHERE project = ? AND branch = ? AND number < ? AND status IN ('succeeded', 'failed') ORDER BY number DESC LIMIT 1`, project, branch, number).Scan(&status)
	return err == nil && status == string(RunFailed)
}
