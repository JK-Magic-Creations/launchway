package engine

// Lanes (schema 24): run-level concurrency. A pipeline's lane says that only
// one run of the lane goes at a time, and what a run that arrives while the
// lane is held does: wait its turn, replace what is there, or be skipped.
//
// A run joins its lane in the transaction that creates it. That
// transaction has taken the project's run_counters row, which stays locked
// until it ends, so runs created at the same moment on any server take
// turns, as they do for supersede. A run that is held back is created with
// status queued and none of its steps queued, and what happened is recorded
// by run.lane_* events; lane_holders and lane_waiting are derived from them
// as deploy_locks and deploy_queue are from the deploy events. When a
// holder ends, in the same transaction the first run waiting for its lane
// starts. A promotion lost to a crash cannot wedge a lane: every server's
// reaper looks for lanes that have waiting runs and no live holder, and
// starts the first of them (recoverLanes).

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// isLane reports whether the event is one of the lane events.
func (t EventType) isLane() bool {
	return t == RunLaneWaiting || t == RunLaneStarted || t == RunLaneReplaced || t == RunLaneSkipped
}

// applyLane moves a run's place in its lane by one event.
func (st *RunState) applyLane(ev Event) {
	l := ev.Data.Lane
	if l == nil {
		st.notUnderstood("a %s event without a lane", ev.Type)
		return
	}
	if st.Lane == nil || st.Lane.Key != l.Key {
		st.Lane = &LaneState{Name: l.Name, Key: l.Key, Label: l.Label}
	}
	cur := st.Lane
	switch ev.Type {
	case RunLaneWaiting:
		cur.State, cur.Other, cur.OtherNumber, cur.Since = LaneWaiting, l.Run, l.Number, ev.At
	case RunLaneStarted:
		if cur.State == LaneWaiting && ev.At > cur.Since {
			st.Excluded += ev.At - cur.Since // the timeout does not run while a run waits for its lane
		}
		cur.State, cur.Other, cur.OtherNumber, cur.Since = LaneHolding, "", 0, ev.At
	case RunLaneReplaced:
		cur.State, cur.Other, cur.OtherNumber, cur.Since = LaneReplaced, l.Run, l.Number, ev.At
	case RunLaneSkipped:
		cur.State, cur.Other, cur.OtherNumber, cur.Since = LaneSkipped, l.Run, l.Number, ev.At
	}
}

// laneEvent is the payload of a lane event of this run's lane.
func laneEvent(st *RunState, other string, number int) *LaneEvent {
	return &LaneEvent{Name: st.Lane.Name, Key: st.Lane.Key, Label: st.Lane.Label, Run: other, Number: number}
}

// lockLanes takes the project's run_counters row, so that creating a run,
// releasing a lane and resuming a run of the project take turns.
func (c *cmd) lockLanes(project string) error {
	_, err := c.tx.Exec(`INSERT INTO run_counters(project, last) VALUES(?, 0) ON CONFLICT(project) DO UPDATE SET last = run_counters.last`, project)
	return err
}

// activeHolder is the run that holds a lane, if one does. A holder row whose
// run is over is not a holder: it is removed.
func (c *cmd) activeHolder(project, key string) (run string, number int, found bool, err error) {
	err = c.tx.QueryRow(`SELECT run_id FROM lane_holders WHERE project = ? AND lane_key = ?`, project, key).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	var status string
	err = c.tx.QueryRow(`SELECT number, status FROM runs WHERE id = ?`, run).Scan(&number, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, err
	}
	if errors.Is(err, sql.ErrNoRows) || RunStatus(status).Terminal() {
		if _, err := c.tx.Exec(`DELETE FROM lane_holders WHERE project = ? AND lane_key = ? AND run_id = ?`, project, key, run); err != nil {
			return "", 0, false, err
		}
		return "", 0, false, nil
	}
	return run, number, true, nil
}

// waitingRuns lists the runs waiting for a lane, first in line first.
func (c *cmd) waitingRuns(project, key string) ([]string, error) {
	rows, err := c.tx.Query(`SELECT run_id FROM lane_waiting WHERE project = ? AND lane_key = ? ORDER BY position`, project, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// joinLane decides, for a run being created, whether it goes now: it takes
// a free lane; or, if another run holds it, waits, replaces it, or is
// skipped, as the pipeline's lane says. It reports whether the run goes (so
// that its steps are queued), and a note for the decision made about its
// push or firing.
func (c *cmd) joinLane(project string, l *pipeline.Lane, trig Trigger, number int) (goes bool, note string, err error) {
	key := l.LaneKey(trig.Ref, trig.PullRequest, trig.Inputs)
	label := l.Label(trig.Ref, trig.PullRequest, trig.Inputs)
	here := &LaneEvent{Name: l.Name, Key: key, Label: label}
	if err := c.lockLanes(project); err != nil {
		return false, "", err
	}
	holder, holderNumber, held, err := c.activeHolder(project, key)
	if err != nil {
		return false, "", err
	}
	if !held {
		// A lane with runs waiting and no holder was left by a lost
		// promotion: they were first.
		if waiting, err := c.waitingRuns(project, key); err != nil {
			return false, "", err
		} else if len(waiting) > 0 {
			started, err := c.promoteLane(project, key)
			if err != nil {
				return false, "", err
			}
			if started {
				if holder, holderNumber, held, err = c.activeHolder(project, key); err != nil {
					return false, "", err
				}
			}
		}
	}
	if !held {
		c.emit(RunLaneStarted, Payload{Lane: here})
		return true, "", nil
	}
	here.Run, here.Number = holder, holderNumber
	switch l.WhenBusy {
	case pipeline.LaneSkip:
		c.emit(RunLaneSkipped, Payload{Lane: here})
		c.emit(RunCancelRequested, Payload{Reason: fmt.Sprintf("skipped: lane busy (run #%d)", holderNumber)})
		return true, fmt.Sprintf("skipped: lane %s is busy (run #%d)", label, holderNumber), nil
	case pipeline.LaneCancel:
		waiting, err := c.waitingRuns(project, key)
		if err != nil {
			return false, "", err
		}
		for _, id := range waiting {
			if err := c.displaceInLane(id, number, label); err != nil {
				return false, "", err
			}
		}
		kept := false
		if err := c.alsoRun(holder, func(o *cmd) error {
			if o.st.holdsDeployment() {
				kept = true // it finishes its deployment first; the new run waits
				return nil
			}
			if o.st.Status.Terminal() || o.st.CancelRequested {
				return nil
			}
			o.emit(RunLaneReplaced, Payload{Lane: laneEvent(o.st, c.runID, number)})
			o.emit(RunCancelRequested, Payload{Reason: fmt.Sprintf("replaced in lane %s by run #%d", label, number),
				SupersededBy: &Superseder{Run: c.runID, Number: number}})
			o.advance()
			return o.supersededEvent(fmt.Sprintf("replaced in lane %s by run #%d", label, number))
		}); err != nil {
			return false, "", err
		}
		if kept {
			c.emit(RunLaneWaiting, Payload{Lane: here})
			return false, fmt.Sprintf("waiting for lane %s: run #%d finishes its deployment first", label, holderNumber), nil
		}
		c.emit(RunLaneStarted, Payload{Lane: &LaneEvent{Name: l.Name, Key: key, Label: label}})
		return true, fmt.Sprintf("took lane %s from run #%d", label, holderNumber), nil
	}
	// wait
	if l.Queue != pipeline.LaneAll {
		waiting, err := c.waitingRuns(project, key)
		if err != nil {
			return false, "", err
		}
		for _, id := range waiting {
			if err := c.displaceInLane(id, number, label); err != nil {
				return false, "", err
			}
		}
	}
	c.emit(RunLaneWaiting, Payload{Lane: here})
	return false, fmt.Sprintf("waiting for lane %s (run #%d is running)", label, holderNumber), nil
}

// displaceInLane cancels a waiting run because the run with the given number
// took its place in the lane.
func (c *cmd) displaceInLane(runID string, number int, label string) error {
	return c.alsoRun(runID, func(o *cmd) error {
		if o.st.Status.Terminal() || o.st.CancelRequested || !o.st.laneWaiting() {
			return nil
		}
		o.emit(RunLaneReplaced, Payload{Lane: laneEvent(o.st, c.runID, number)})
		o.emit(RunCancelRequested, Payload{Reason: fmt.Sprintf("replaced in lane %s by run #%d", label, number),
			SupersededBy: &Superseder{Run: c.runID, Number: number}})
		o.advance()
		return o.supersededEvent(fmt.Sprintf("replaced in lane %s by run #%d", label, number))
	})
}

// startFromLane starts a waiting run (this command's run, or one it changes
// with alsoRun) if its lane is free and it is first in line.
func (c *cmd) startFromLane() (bool, error) {
	if !c.st.laneWaiting() || c.st.Status.Terminal() || c.st.CancelRequested {
		return false, nil
	}
	_, _, held, err := c.activeHolder(c.st.Project, c.st.Lane.Key)
	if err != nil || held {
		return false, err
	}
	waiting, err := c.waitingRuns(c.st.Project, c.st.Lane.Key)
	if err != nil {
		return false, err
	}
	if len(waiting) > 0 && waiting[0] != c.runID {
		return false, nil
	}
	c.emit(RunLaneStarted, Payload{Lane: laneEvent(c.st, "", 0)})
	c.advance()
	return true, nil
}

// promoteLane starts the first run waiting for a lane, after its holder
// ended. It takes the project's run_counters row first, so that a run being
// created at the same moment either sees this one holding the lane or is
// seen waiting. It reports whether it started one.
func (c *cmd) promoteLane(project, key string) (bool, error) {
	if err := c.lockLanes(project); err != nil {
		return false, err
	}
	for {
		if _, _, held, err := c.activeHolder(project, key); err != nil || held {
			return false, err
		}
		waiting, err := c.waitingRuns(project, key)
		if err != nil || len(waiting) == 0 {
			return false, err
		}
		next := waiting[0]
		called, started := false, false
		err = c.alsoRun(next, func(o *cmd) error {
			called = true
			var err error
			started, err = o.startFromLane()
			return err
		})
		if err != nil {
			return false, err
		}
		if started {
			return true, nil
		}
		if !called {
			return false, nil // a run this server does not understand: a newer server starts it
		}
		// Not waiting any more (it was canceled, or never was): forget it.
		if _, err := c.tx.Exec(`DELETE FROM lane_waiting WHERE run_id = ?`, next); err != nil {
			return false, err
		}
	}
}

// persistLanes brings lane_holders, lane_waiting and the lane columns of
// runs up to date with the command's events. A holder that ends releases
// its lane, and the first run waiting for it starts, in this transaction.
func (c *cmd) persistLanes() error {
	st := c.st
	touched, released := false, false
	for _, ev := range c.events {
		switch ev.Type {
		case RunLaneStarted:
			touched = true
			l := ev.Data.Lane
			if _, err := c.tx.Exec(`DELETE FROM lane_waiting WHERE run_id = ?`, st.ID); err != nil {
				return err
			}
			if _, err := c.tx.Exec(`INSERT INTO lane_holders(project, lane_key, run_id, since) VALUES(?,?,?,?)
				ON CONFLICT(project, lane_key) DO UPDATE SET run_id = excluded.run_id, since = excluded.since`,
				st.Project, l.Key, st.ID, ev.At); err != nil {
				return err
			}
		case RunLaneWaiting:
			touched = true
			l := ev.Data.Lane
			if _, err := c.tx.Exec(`INSERT INTO lane_waiting(run_id, project, lane_key, position, since)
				VALUES(?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM lane_waiting WHERE project = ? AND lane_key = ?), ?)
				ON CONFLICT(run_id) DO NOTHING`, st.ID, st.Project, l.Key, st.Project, l.Key, ev.At); err != nil {
				return err
			}
		case RunLaneReplaced, RunLaneSkipped:
			touched = true
			if _, err := c.tx.Exec(`DELETE FROM lane_waiting WHERE run_id = ?`, st.ID); err != nil {
				return err
			}
			if _, err := c.tx.Exec(`DELETE FROM lane_holders WHERE run_id = ?`, st.ID); err != nil {
				return err
			}
		case RunFinished:
			if st.Lane == nil {
				continue
			}
			if _, err := c.tx.Exec(`DELETE FROM lane_waiting WHERE run_id = ?`, st.ID); err != nil {
				return err
			}
			res, err := c.tx.Exec(`DELETE FROM lane_holders WHERE run_id = ?`, st.ID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				released = true
			}
		}
	}
	if touched && st.Lane != nil {
		if _, err := c.tx.Exec(`UPDATE runs SET lane_name = ?, lane_key = ?, lane_label = ?, lane_state = ?, lane_other = ?, lane_other_number = ? WHERE id = ?`,
			st.Lane.Name, st.Lane.Key, st.Lane.Label, st.Lane.State, st.Lane.Other, st.Lane.OtherNumber, st.ID); err != nil {
			return err
		}
	}
	if released {
		if _, err := c.promoteLane(st.Project, st.Lane.Key); err != nil {
			return err
		}
	}
	return nil
}

// recoverLanes starts the first run of each lane that has runs waiting and no
// holder that is still going: the holder ended, and the transaction that
// should have started the next run was lost. Every server runs it; the run
// is started by an event, so one wins.
func (e *Engine) recoverLanes() {
	if !e.knows(schemaFlow) {
		return
	}
	rows, err := e.db.Query(`SELECT w.run_id FROM lane_waiting w
		WHERE w.position = (SELECT MIN(x.position) FROM lane_waiting x WHERE x.project = w.project AND x.lane_key = w.lane_key)
		AND NOT EXISTS (SELECT 1 FROM lane_holders h JOIN runs r ON r.id = h.run_id
			WHERE h.project = w.project AND h.lane_key = w.lane_key AND r.status IN ('queued', 'running'))`)
	if err != nil {
		e.log.Error("looking for lanes with no holder", "err", err)
		return
	}
	defer rows.Close()
	var first []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.log.Error("looking for lanes with no holder", "err", err)
			return
		}
		first = append(first, id)
	}
	if err := rows.Err(); err != nil {
		e.log.Error("looking for lanes with no holder", "err", err)
		return
	}
	for _, id := range first {
		err := e.withRun(id, func(c *cmd) error {
			if err := c.lockLanes(c.st.Project); err != nil {
				return err
			}
			started, err := c.startFromLane()
			if err == nil && !started && !c.st.laneWaiting() {
				_, err = c.tx.Exec(`DELETE FROM lane_waiting WHERE run_id = ?`, id) // not waiting after all
			}
			return err
		})
		if err != nil && !errors.Is(err, ErrNeedsNewer) {
			e.log.Error("starting a run whose lane was left free", "run", id, "err", err)
		}
	}
}

// ---- reading ----

// LaneInfo is a run's place in its lane, as shown with the run.
type LaneInfo struct {
	Name  string `json:"name"`
	Key   string `json:"key"`
	Label string `json:"label"`
	// State is holding, waiting, replaced or skipped.
	State string `json:"state"`
	// Position is 1 for the run next in line, for a waiting run.
	Position int `json:"position,omitempty"`
	// Behind is, for a waiting or skipped run, the run that holds the lane;
	// for a replaced one, the run that took its place.
	Behind *LaneRunRef `json:"behind,omitempty"`
	// Reason says in words why a waiting run waits.
	Reason string `json:"reason,omitempty"`
}

// LaneRunRef names a run in a lane.
type LaneRunRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
}

// LaneView is one lane of a project: who holds it, and who waits.
type LaneView struct {
	Name    string           `json:"name"`
	Key     string           `json:"key"`
	Label   string           `json:"label"`
	Holder  *LaneRunSummary  `json:"holder,omitempty"`
	Waiting []LaneRunSummary `json:"waiting"`
}

// LaneRunSummary is a run in a lane.
type LaneRunSummary struct {
	ID       string    `json:"id"`
	Number   int       `json:"number"`
	Status   RunStatus `json:"status"`
	Event    string    `json:"event"`
	Since    int64     `json:"since"`
	Position int       `json:"position,omitempty"`
}

// laneInfo builds the lane block of a run from the lane_* columns.
func laneInfo(name, key, label, state, other string, otherNumber, position int) *LaneInfo {
	if key == "" && state == "" {
		return nil
	}
	l := &LaneInfo{Name: name, Key: key, Label: label, State: state, Position: position}
	if other != "" || otherNumber > 0 {
		l.Behind = &LaneRunRef{ID: other, Number: otherNumber}
	}
	if state == LaneWaiting && otherNumber > 0 {
		l.Reason = fmt.Sprintf("waiting for lane %s (run #%d is running)", label, otherNumber)
	}
	return l
}

// Lanes lists a project's lanes that have a holder or runs waiting.
func (e *Engine) Lanes(project string) ([]LaneView, error) {
	if !e.knows(schemaFlow) {
		return []LaneView{}, nil
	}
	byKey := map[string]*LaneView{}
	var order []string
	get := func(key, name, label string) *LaneView {
		if v := byKey[key]; v != nil {
			return v
		}
		v := &LaneView{Name: name, Key: key, Label: label, Waiting: []LaneRunSummary{}}
		byKey[key] = v
		order = append(order, key)
		return v
	}
	if err := func() error {
		rows, err := e.db.Query(`SELECT h.lane_key, r.lane_name, r.lane_label, r.id, r.number, r.status, r.event, h.since
		FROM lane_holders h JOIN runs r ON r.id = h.run_id WHERE h.project = ? ORDER BY h.lane_key`, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, name, label string
			var s LaneRunSummary
			if err := rows.Scan(&key, &name, &label, &s.ID, &s.Number, &s.Status, &s.Event, &s.Since); err != nil {
				return err
			}
			get(key, name, label).Holder = &s
		}
		return rows.Err()
	}(); err != nil {
		return nil, err
	}
	rows, err := e.db.Query(`SELECT w.lane_key, r.lane_name, r.lane_label, r.id, r.number, r.status, r.event, w.since, w.position
		FROM lane_waiting w JOIN runs r ON r.id = w.run_id WHERE w.project = ? ORDER BY w.lane_key, w.position`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var key, name, label string
		var s LaneRunSummary
		var pos int
		if err := rows.Scan(&key, &name, &label, &s.ID, &s.Number, &s.Status, &s.Event, &s.Since, &pos); err != nil {
			return nil, err
		}
		seen[key]++
		s.Position = seen[key]
		v := get(key, name, label)
		v.Waiting = append(v.Waiting, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]LaneView, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out, nil
}

// laneRunning is the lanes' state a resumed run needs: refuse the resume
// while another run holds its lane, else take the lane back.
func (c *cmd) takeLaneForResume() error {
	if c.st.Lane == nil || c.st.Draft != nil || !c.e.knows(schemaFlow) {
		return nil
	}
	project, key := c.st.Project, c.st.Lane.Key
	if err := c.lockLanes(project); err != nil {
		return err
	}
	holder, number, held, err := c.activeHolder(project, key)
	if err != nil {
		return err
	}
	if held && holder != c.runID {
		return fmt.Errorf("%w: lane %s is held by run #%d: resume this run once that has finished, or cancel it", ErrConflict, c.st.Lane.Label, number)
	}
	c.emit(RunLaneStarted, Payload{Lane: laneEvent(c.st, "", 0)})
	return nil
}

// laneDetail is what a lane adds to the decision recorded for a push.
func laneDetail(note string) string {
	if note == "" {
		return ""
	}
	return " (" + note + ")"
}

// noteLaneFiring says on a schedule's firing that its run waits for a lane
// or was skipped by it.
func (c *cmd) noteLaneFiring(nr *NewRun, note string) error {
	if nr.Fire == nil || note == "" {
		return nil
	}
	outcome := "started"
	if strings.HasPrefix(note, "skipped") {
		outcome = "skipped"
	}
	_, err := c.tx.Exec(`UPDATE schedule_fires SET outcome = ?, detail = detail || ? WHERE project = ? AND sched_key = ? AND fire_at = ?`,
		outcome, laneDetail(note), nr.Project, nr.Fire.Key, nr.Fire.At)
	return err
}

// attachLanePositions says how far back in line each waiting run is.
func (e *Engine) attachLanePositions(runs []*RunSummary) error {
	if !e.knows(schemaFlow) {
		return nil
	}
	for _, r := range runs {
		if r.Lane == nil || r.Lane.State != LaneWaiting {
			continue
		}
		var n int
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM lane_waiting x WHERE x.project = ? AND x.lane_key = ?
			AND x.position <= (SELECT position FROM lane_waiting WHERE run_id = ?)`, r.Project, r.Lane.Key, r.ID).Scan(&n); err != nil {
			return err
		}
		r.Lane.Position = n
	}
	return nil
}

// lockLaneBeforeRows takes the project's run_counters row before anything of
// the run is written, when the command ends a run that holds a lane. Creating
// a run takes that row first and then writes the rows of the runs it
// supersedes; a holder that ended wrote its own row first and asked for the
// counter second, so the two could wait for each other on PostgreSQL. Every
// path now takes run_counters first, then run rows.
func (c *cmd) lockLaneBeforeRows() error {
	if (c.st.Lane == nil && c.st.Queue == nil) || !c.e.knows(schemaFlow) {
		return nil
	}
	for _, ev := range c.events {
		if ev.Type == RunFinished {
			return c.lockLanes(c.st.Project)
		}
	}
	return nil
}

// takeLane has a run being created join its pipeline's lane (schema 24): it
// takes the lane, waits for it, or is skipped, before any of its steps is
// queued. Draft runs never join one. It reports whether the run goes now, and
// the note to add to the decision about its push or firing.
func (c *cmd) takeLane(nr *NewRun, draft bool, trig Trigger, number int) (goes bool, note string, err error) {
	l := nr.Pipeline.Lane
	if l == nil || draft || !c.e.knows(schemaFlow) {
		return true, "", nil
	}
	if goes, note, err = c.joinLane(nr.Project, l, trig, number); err != nil {
		return false, "", err
	}
	return goes, note, c.noteLaneFiring(nr, note)
}
