package engine

// Run inputs, loops, schedules and webhooks in the engine (schema 13).
//
// Inputs are recorded with the run, in the trigger of its run.created event,
// and its pipeline declares them; so does a resume, which keeps them, and a
// re-run, which copies the trigger. Loops are expanded before a run is
// created: the engine sees ordinary steps, and the pipeline's Loops say
// which steps are members of which loop, for max_parallel and fail_fast.
// A schedule's firing and a webhook's delivery are claimed in the
// transaction that creates the run, like a push, so exactly one server acts
// on each.

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Firing is a schedule firing a run is for (NewRun.Fire).
type Firing struct {
	Key string // the schedule's key (pipeline.Schedule.Key)
	At  int64  // the time it fires for, Unix ms
}

// Delivery is a webhook call a run is for (NewRun.Delivery): the trigger's
// name and the caller's idempotency key, with a hash of the body so that a
// repeat of the key with another body can be told apart.
type Delivery struct {
	Hook     string
	Key      string
	BodyHash string
}

// usesSchema13 reports whether a run.created event needs a server that
// knows schema 13: its pipeline uses inputs, loops, schedules or webhooks,
// or its trigger is a schedule or a webhook, or carries input values.
func usesSchema13(p Payload) bool {
	if p.Pipeline != nil && p.Pipeline.UsesSchema13() {
		return true
	}
	if t := p.Trigger; t != nil {
		return t.Event == pipeline.EventSchedule || t.Event == pipeline.EventWebhook || len(t.Inputs) > 0
	}
	return false
}

// checkNewRun checks what schema 13 adds to a new run.
func (e *Engine) checkNewRun(nr *NewRun) error {
	p := nr.Pipeline
	if p.UsesSchemaCache() && !e.knows(schemaCache) {
		return fmt.Errorf("%w: this server does not know build caches (cache:, schema %d)", ErrConflict, schemaCache)
	}
	if !p.Bound() {
		return fmt.Errorf("%w: the pipeline has loops that have not been repeated over their lists; bind it first", ErrConflict)
	}
	needs := usesSchema13(Payload{Pipeline: p, Trigger: &nr.Trigger}) || nr.Fire != nil || nr.Delivery != nil
	if needs && !e.knows(schemaTriggers) {
		return fmt.Errorf("%w: this server does not know run inputs, loops, schedules or webhooks (schema %d)", ErrConflict, schemaTriggers)
	}
	if len(p.Inputs) > 0 || len(nr.Trigger.Inputs) > 0 {
		vals := nr.Trigger.Inputs
		if vals == nil {
			vals = map[string]string{}
		}
		if err := pipeline.CheckValues(p, vals); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	ev := nr.Trigger.Event
	if (nr.Fire != nil) != (ev == pipeline.EventSchedule) {
		return fmt.Errorf("%w: a run of event schedule is for one firing of a schedule, and only such a run is", ErrConflict)
	}
	if nr.Delivery != nil && ev != pipeline.EventWebhook {
		return fmt.Errorf("%w: an idempotency key goes with a run of event webhook", ErrConflict)
	}
	if nr.Push != nil && (nr.Fire != nil || nr.Delivery != nil) {
		return fmt.Errorf("%w: a run is for one push, firing or webhook call", ErrConflict)
	}
	return nil
}

// claimFire claims a schedule's firing for the run being created, in its
// transaction. It reports false if another server claimed it first.
func claimFire(tx *txx, project string, f *Firing, runID string, now int64, detail string) (bool, error) {
	res, err := tx.Exec(`INSERT INTO schedule_fires(project, sched_key, fire_at, at, outcome, detail, run_id) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT DO NOTHING`, project, f.Key, f.At, now, "started", detail, runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// claimDelivery claims a webhook call's idempotency key for the run being
// created, in its transaction. It reports false if the key was used
// already.
func claimDelivery(tx *txx, project string, d *Delivery, runID string, now int64) (bool, error) {
	res, err := tx.Exec(`INSERT INTO webhook_deliveries(project, hook, delivery, at, body_hash, run_id) VALUES(?,?,?,?,?,?)
		ON CONFLICT DO NOTHING`, project, "trigger:"+d.Hook, d.Key, now, d.BodyHash, runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// claimNewRun claims whatever the run being created is for, other than a
// push: a firing or an idempotency key. It returns ErrAlreadyClaimed if
// another call got there first.
func (c *cmd) claimNewRun(nr *NewRun, number int) error {
	switch {
	case nr.Fire != nil:
		ok, err := claimFire(c.tx, nr.Project, nr.Fire, c.runID, c.now, fmt.Sprintf("started run #%d", number))
		if err != nil {
			return err
		}
		if !ok {
			return ErrAlreadyClaimed
		}
	case nr.Delivery != nil:
		ok, err := claimDelivery(c.tx, nr.Project, nr.Delivery, c.runID, c.now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrAlreadyClaimed
		}
	}
	return nil
}

// ---- loops while the run goes ----

// loopHolds reports whether a step must wait before it is queued because
// a loop it belongs to has max_parallel members going already. A member
// that is going (one of its steps is queued, running or waiting) may go on.
func (c *cmd) loopHolds(stepID string) bool {
	p := c.st.Pipeline
	if len(p.Loops) == 0 {
		return false
	}
	for _, ref := range p.LoopsOf(stepID) {
		l := ref.Loop
		if l.MaxParallel <= 0 {
			continue
		}
		active := 0
		mine := false
		for i, m := range l.Members {
			if c.memberGoing(m) {
				active++
				mine = mine || i == ref.Member
			}
		}
		if !mine && active >= l.MaxParallel {
			return true
		}
	}
	return false
}

// memberGoing reports whether any step of a loop member is queued, held by
// a runner, or waiting for a decision.
func (c *cmd) memberGoing(m pipeline.LoopMember) bool {
	for _, id := range c.st.Order {
		if !m.Has(id) {
			continue
		}
		switch c.st.Steps[id].Status {
		case StepQueuedS, StepLeasedS, StepRunning, StepWaitingS:
			return true
		}
	}
	return false
}

// failFast stops the other members of a fail_fast loop once a member has a
// step that failed for good: steps not started are cancelled, and running
// ones are stopped (their lease ends, so their runner stops them at its
// next heartbeat). It reports whether it changed anything.
func (c *cmd) failFast() bool {
	st := c.st
	if len(st.Pipeline.Loops) == 0 || st.CancelRequested {
		return false
	}
	changed := false
	for _, l := range st.Pipeline.Loops {
		if !l.FailFast {
			continue
		}
		failed := ""
		for _, m := range l.Members {
			for _, id := range st.Order {
				if m.Has(id) && st.Steps[id].Status == StepFailed {
					failed = id
					break
				}
			}
			if failed != "" {
				break
			}
		}
		if failed == "" {
			continue
		}
		reason := fmt.Sprintf("stopped because %s failed (the loop %s has fail_fast)", failed, l.ID)
		for _, m := range l.Members {
			if m.Has(failed) {
				continue
			}
			for _, id := range st.Order {
				if !m.Has(id) {
					continue
				}
				s := st.Steps[id]
				switch s.Status {
				case StepPending, StepQueuedS, StepWaitingS:
					c.emit(StepCanceled, Payload{Step: id, Reason: reason})
					changed = true
				case StepLeasedS, StepRunning:
					if s.LeaseID != "" {
						if _, err := c.tx.Exec(`DELETE FROM leases WHERE id = ?`, s.LeaseID); err != nil {
							c.dbFail(err)
						}
					}
					c.dropTests(id, s.Attempt)
					c.emit(StepCanceled, Payload{Step: id, Attempt: s.Attempt, Reason: reason})
					changed = true
				}
			}
		}
	}
	return changed
}

// ---- reading back ----

// TriggerDetail is what a run was started with, beyond its event and ref.
type TriggerDetail struct {
	Inputs       map[string]string `json:"inputs,omitempty"`
	Schedule     string            `json:"schedule,omitempty"`
	ScheduledFor int64             `json:"scheduled_for,omitempty"`
	Webhook      string            `json:"webhook,omitempty"`
}

func triggerDetail(t Trigger) *TriggerDetail {
	if len(t.Inputs) == 0 && t.Schedule == "" && t.Webhook == "" {
		return nil
	}
	return &TriggerDetail{Inputs: t.Inputs, Schedule: t.Schedule, ScheduledFor: t.ScheduledFor, Webhook: t.Webhook}
}

// RunTrigger returns what a finished or running run was started with: its
// trigger, input values included. Re-runs and the CLI use it.
func (e *Engine) RunTrigger(runID string) (Trigger, error) {
	evs, _, err := loadEvents(e.db, runID)
	if err != nil {
		return Trigger{}, err
	}
	st := e.fold(evs)
	if st == nil {
		return Trigger{}, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return st.Trigger, nil
}

// ---- schedules ----

// ScheduleFire is one firing of a schedule, as recorded.
type ScheduleFire struct {
	Key     string `json:"key"`
	FireAt  int64  `json:"fire_at"`
	At      int64  `json:"at"`
	Outcome string `json:"outcome"` // started, error or skipped
	Detail  string `json:"detail"`
	RunID   string `json:"run_id,omitempty"`
}

// ScheduleSeen records that a project's schedule exists, if no server has
// yet, and returns when it was first seen and the last time it fired for (0
// if never).
func (e *Engine) ScheduleSeen(project, key string, now int64) (firstSeen, lastFire int64, err error) {
	if !e.knows(schemaTriggers) {
		return 0, 0, fmt.Errorf("%w: this server does not know schedules (schema %d)", ErrConflict, schemaTriggers)
	}
	unlock := e.writeLock()
	_, err = e.db.Exec(`INSERT INTO schedule_seen(project, sched_key, first_seen) VALUES(?,?,?) ON CONFLICT DO NOTHING`, project, key, now)
	unlock()
	if err != nil {
		return 0, 0, err
	}
	if err := e.db.QueryRow(`SELECT first_seen FROM schedule_seen WHERE project = ? AND sched_key = ?`, project, key).Scan(&firstSeen); err != nil {
		return 0, 0, err
	}
	var last sql.NullInt64
	if err := e.db.QueryRow(`SELECT MAX(fire_at) FROM schedule_fires WHERE project = ? AND sched_key = ?`, project, key).Scan(&last); err != nil {
		return 0, 0, err
	}
	return firstSeen, last.Int64, nil
}

// RecordFire records a firing that started no run (the pipeline at the ref
// had problems, say), claiming it as CreateRun would. It reports false if
// another server claimed the firing first.
func (e *Engine) RecordFire(project string, f Firing, outcome, detail string) (bool, error) {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO schedule_fires(project, sched_key, fire_at, at, outcome, detail, run_id) VALUES(?,?,?,?,?,?,'')
		ON CONFLICT DO NOTHING`, project, f.Key, f.At, e.now().UnixMilli(), outcome, clipDetail(detail))
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	return true, tx.Commit()
}

func clipDetail(s string) string {
	if len(s) > 4000 {
		return s[:4000] + "…"
	}
	return s
}

// keepFires is how many firings of each schedule are kept.
const keepFires = 20

// ScheduleFires lists a project's recent firings, newest first, at most
// keepFires per schedule.
func (e *Engine) ScheduleFires(project string) ([]ScheduleFire, error) {
	if !e.knows(schemaTriggers) {
		return []ScheduleFire{}, nil
	}
	rows, err := e.db.Query(`SELECT sched_key, fire_at, at, outcome, detail, run_id FROM schedule_fires
		WHERE project = ? ORDER BY fire_at DESC LIMIT 500`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleFire{}
	per := map[string]int{}
	for rows.Next() {
		var f ScheduleFire
		if err := rows.Scan(&f.Key, &f.FireAt, &f.At, &f.Outcome, &f.Detail, &f.RunID); err != nil {
			return nil, err
		}
		if per[f.Key]++; per[f.Key] <= keepFires {
			out = append(out, f)
		}
	}
	return out, rows.Err()
}

// PruneFires removes old firings, keeping keepFires per schedule, and
// deliveries older than keep.
func (e *Engine) PruneTriggers(olderThan int64) {
	if !e.knows(schemaTriggers) {
		return
	}
	unlock := e.writeLock()
	defer unlock()
	if _, err := e.db.Exec(`DELETE FROM schedule_fires WHERE at < ? AND fire_at NOT IN
		(SELECT f2.fire_at FROM schedule_fires f2 WHERE f2.project = schedule_fires.project AND f2.sched_key = schedule_fires.sched_key
		 ORDER BY f2.fire_at DESC LIMIT ?)`, olderThan, keepFires); err != nil {
		e.log.Warn("pruning schedule firings", "err", err)
	}
	if _, err := e.db.Exec(`DELETE FROM webhook_deliveries WHERE at < ?`, olderThan); err != nil {
		e.log.Warn("pruning webhook deliveries", "err", err)
	}
}

// ---- webhook deliveries ----

// DeliveryRun returns the run a webhook trigger's idempotency key started,
// and the hash of the body it came with; ErrNotFound if the key is unused.
func (e *Engine) DeliveryRun(project, hook, key string) (runID, bodyHash string, err error) {
	err = e.db.QueryRow(`SELECT run_id, body_hash FROM webhook_deliveries WHERE project = ? AND hook = ? AND delivery = ?`,
		project, "trigger:"+hook, key).Scan(&runID, &bodyHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: delivery", ErrNotFound)
	}
	return runID, bodyHash, err
}

// SeeDelivery records a Git host's webhook delivery, by its ID, and reports
// whether it is new: a delivery seen before is a replay.
func (e *Engine) SeeDelivery(project, host, id string) (bool, error) {
	if !e.knows(schemaTriggers) {
		return false, fmt.Errorf("%w: this server does not know webhooks (schema %d)", ErrConflict, schemaTriggers)
	}
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`INSERT INTO webhook_deliveries(project, hook, delivery, at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`,
		project, "git:"+host, id, e.now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// inputEnv is the run's input values as variables.
func inputEnv(t Trigger, vars map[string]InputVar) {
	for name, v := range t.Inputs {
		k := pipeline.InputEnv(name)
		vars[k] = InputVar{Name: k, Value: v, From: FromInput}
	}
}

// eachEnv is a loop member's values as variables.
func eachEnv(def *pipeline.Step, vars map[string]InputVar) {
	for key, v := range def.Each {
		k := "LAUNCHWAY_EACH_" + pipeline.EnvName(key)
		vars[k] = InputVar{Name: k, Value: v, From: FromEach}
	}
}

// Where a variable came from, for schema 13.
const (
	FromInput = "input" // a run input
	FromEach  = "each"  // a loop member's value
)

// Triggers reports whether this server knows run inputs, loops, schedules
// and webhooks (schema 13).
func (e *Engine) Triggers() bool { return e.knows(schemaTriggers) }

// upstreamGate decides, for a step whose when says upstream: failed or
// always, whether the steps it needs let it run: it waits until all of them
// have ended, however they ended. With failed, it runs only if one of them
// failed, was cancelled, or was not run because of a failure; otherwise
// condSkip says why it is skipped.
func (c *cmd) upstreamGate(def *pipeline.Step, rule string) (ready bool, blocked, condSkip string) {
	failure := false
	for _, n := range def.Needs {
		dep := c.st.Steps[n]
		if !dep.Status.Terminal() {
			return false, "", ""
		}
		if dep.Status == StepFailed || dep.Status == StepCanceledS || (dep.Status == StepSkippedS && dep.SkipKind == "upstream") {
			failure = true
		}
	}
	if rule == pipeline.UpstreamFailed && !failure {
		return true, "", "none of the steps it needs failed"
	}
	return true, "", ""
}

// resetAfterUpstream, on a resume, also resets the steps that ran after a
// step that runs again: steps with when: { upstream: always } or failed,
// and whatever needs them. A cleanup step that ran after a failure thus
// runs again after the resumed steps, and decides again whether to run.
// Only steps whose upstream rule let them run past a failure can have
// ended while something they need did not succeed, so for pipelines
// without such steps this changes nothing.
func (c *cmd) resetAfterUpstream() {
	for changed := true; changed; {
		changed = false
		for _, id := range c.st.Order {
			s := c.st.Steps[id]
			if s.Status == StepPending || !s.Status.Terminal() || s.ReusedFrom != "" {
				continue
			}
			def := c.st.Pipeline.Step(id)
			for _, n := range def.Needs {
				if c.st.Steps[n].Status == StepPending {
					c.emit(StepReset, Payload{Step: id})
					changed = true
					break
				}
			}
		}
	}
}
