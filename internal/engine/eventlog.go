package engine

// The event log (schema 42): every event the server received, as a record,
// and for each trigger it was judged against, the decision with its reason.
//
// What an event is: a push or a pull request's head moving, a schedule's
// firing, a call to a webhook or an event source, a person starting a run, or
// another project's run ending. What a decision is: one of a closed set
// (started, held, waiting, filtered, superseded, skipped, duplicate, refused,
// error) and a sentence saying why, in words a person can act on.
//
// Guarantee: an event whose decision was a start is written in the same
// transaction as the run it started, so a start with no event, or an event
// that says "started" for a run that does not exist, cannot happen. An event
// that was refused or filtered is written on its own. The payload kept is
// small, with secrets removed (see scrubPayload).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// The kinds of event source.
const (
	SourcePush         = "push"
	SourcePullRequest  = "pull_request"
	SourceSchedule     = "schedule"
	SourceWebhook      = "webhook"
	SourceEventSource  = "event_source"
	SourceManual       = "manual"
	SourceRunResult    = "run_result"
	SourceLaunchResult = "launch_result"
)

// The decisions an event can have for a trigger.
const (
	DecStarted    = "started"    // a run was created
	DecHeld       = "held"       // a run was created but waits: a limit or a lane is busy
	DecWaiting    = "waiting"    // not decided yet: a window is open, or the forge has not answered
	DecFiltered   = "filtered"   // the trigger's rules say this event starts nothing
	DecSuperseded = "superseded" // a newer event took this one's place
	DecSkipped    = "skipped"    // the lane was busy and says skip
	DecDuplicate  = "duplicate"  // the dedupe key was seen before
	DecRefused    = "refused"    // not verified, over a limit, or not permitted
	DecError      = "error"      // the pipeline or the request has problems
)

// Decisions lists them, in the order the page offers them.
var Decisions = []string{DecStarted, DecHeld, DecWaiting, DecFiltered, DecSuperseded, DecSkipped, DecDuplicate, DecRefused, DecError}

const (
	// maxPayload is the most of an event's payload that is kept.
	maxPayload = 16 << 10
	// keepEvents is how many events are kept for each project; unverified
	// ones (which anyone could send) have a smaller allowance.
	keepEvents      = 5000
	keepUnverified  = 200
	maxReasonLength = 1000
)

// EventRecord is one received event.
type EventRecord struct {
	ID         string          `json:"id"`
	Project    string          `json:"project"`
	At         int64           `json:"at"`
	Source     string          `json:"source"`
	SourceName string          `json:"source_name,omitempty"`
	Verified   bool            `json:"verified"`
	Summary    string          `json:"summary"`
	Ref        string          `json:"ref,omitempty"`
	SHA        string          `json:"sha,omitempty"`
	DedupeKey  string          `json:"dedupe_key,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	// Replay is how the event may be started again: the ref (or commit) and the
	// input values the run would be given. Empty when the event cannot be
	// replayed (it was refused, or it started nothing that can be repeated).
	Replay   *ReplayPlan `json:"replay,omitempty"`
	ReplayOf string      `json:"replay_of,omitempty"`
	Actor    string      `json:"actor,omitempty"`
	// Decisions is filled by EventLogEntry and by lists that ask for it.
	Decisions []EventDecision `json:"decisions"`
}

// ReplayPlan is what a replay starts.
type ReplayPlan struct {
	Ref    string            `json:"ref,omitempty"`
	SHA    string            `json:"sha,omitempty"`
	Inputs map[string]string `json:"inputs,omitempty"`
}

// EventDecision is what was decided about an event for one trigger.
type EventDecision struct {
	ID        int64  `json:"id"`
	EventID   string `json:"event_id,omitempty"`
	At        int64  `json:"at"`
	Trigger   string `json:"trigger"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	RunID     string `json:"run_id,omitempty"`
	RunNumber int    `json:"run_number,omitempty"`
}

var secretKey = regexp.MustCompile(`(?i)(secret|token|passw|authorization|api[_-]?key|credential|private[_-]?key|signature|cookie)`)

// scrubPayload returns the JSON payload with the value of every key that
// looks like a credential replaced, cut to what is kept. A body that is not
// JSON is not kept (its size is, in the summary). Nothing here is a promise
// that a payload holds no secret under another name: sources are told not to
// send one, and the page says the payload is shown as kept.
func scrubPayload(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	b, err := json.Marshal(scrubValue(v, 0))
	if err != nil {
		return nil
	}
	if len(b) > maxPayload {
		cut, _ := json.Marshal(map[string]any{"truncated": true, "bytes": len(b)})
		return cut
	}
	return b
}

func scrubValue(v any, depth int) any {
	if depth > 12 {
		return "…"
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			if secretKey.MatchString(k) {
				out[k] = "[removed]"
				continue
			}
			out[k] = scrubValue(e, depth+1)
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for i, e := range x {
			if i >= 100 {
				out = append(out, "…")
				break
			}
			out = append(out, scrubValue(e, depth+1))
		}
		return out
	}
	return v
}

// ScrubPayload is scrubPayload for callers outside the engine.
func ScrubPayload(raw []byte) json.RawMessage { return scrubPayload(raw) }

func clipReason(s string) string {
	if len(s) > maxReasonLength {
		return s[:maxReasonLength] + "…"
	}
	return s
}

// insertEventTx writes an event and its decisions in tx. It returns the
// event's id.
func insertEventTx(tx *txx, ev EventRecord, ds []EventDecision, now int64) (string, error) {
	if ev.ID == "" {
		ev.ID = ids.New("evt")
	}
	ev.At = now
	plan := ""
	if ev.Replay != nil {
		b, _ := json.Marshal(ev.Replay)
		plan = string(b)
	}
	verified := 0
	if ev.Verified {
		verified = 1
	}
	if _, err := tx.Exec(`INSERT INTO event_log(id, project, at, source, source_name, verified, summary, ref, sha, dedupe_key, payload, replay, replay_of, actor)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ev.ID, ev.Project, now, ev.Source, ev.SourceName, verified, clipReason(ev.Summary), ev.Ref, ev.SHA,
		ev.DedupeKey, string(ev.Payload), plan, ev.ReplayOf, ev.Actor); err != nil {
		return "", err
	}
	for _, d := range ds {
		if err := insertEventDecision(tx, ev.ID, ev.Project, d, now); err != nil {
			return "", err
		}
	}
	return ev.ID, nil
}

func insertEventDecision(tx *txx, eventID, project string, d EventDecision, now int64) error {
	_, err := tx.Exec(`INSERT INTO event_decisions(event_id, project, at, trigger_name, decision, reason, run_id, run_number) VALUES(?,?,?,?,?,?,?,?)`,
		eventID, project, now, d.Trigger, d.Decision, clipReason(d.Reason), d.RunID, d.RunNumber)
	return err
}

// noteSuperseded adds a "superseded" decision to the event that started a
// run, saying a newer event took its place. A run with no event (it was made
// before the log existed) has nothing to note.
func noteSuperseded(tx *txx, runID, reason string, now int64) error {
	var eventID, project, trig string
	var number int
	err := tx.QueryRow(`SELECT event_id, project, trigger_name, run_number FROM event_decisions
		WHERE run_id = ? AND decision IN (?, ?) ORDER BY id LIMIT 1`, runID, DecStarted, DecHeld).Scan(&eventID, &project, &trig, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return insertEventDecision(tx, eventID, project, EventDecision{Trigger: trig, Decision: DecSuperseded, Reason: reason, RunID: runID, RunNumber: number}, now)
}

// RecordEvent writes an event that started no run (it was refused, filtered,
// waits in a window, or is a duplicate) with its decisions, and returns its
// id. A run that an event starts is written by CreateRun instead.
func (e *Engine) RecordEvent(ev EventRecord, ds ...EventDecision) (string, error) {
	if !e.knows(schemaEvents) {
		return "", nil
	}
	unlock := e.writeLock()
	tx, err := e.db.Begin()
	if err != nil {
		unlock()
		return "", err
	}
	id, err := insertEventTx(tx, ev, ds, e.now().UnixMilli())
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	unlock()
	if err == nil {
		e.pruneEvents(ev.Project)
	}
	return id, err
}

// AddDecision appends a decision to an existing event (a window that
// closed, a retry).
func (e *Engine) AddDecision(eventID string, d EventDecision) error {
	if !e.knows(schemaEvents) {
		return nil
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var project string
	if err := tx.QueryRow(`SELECT project FROM event_log WHERE id = ?`, eventID).Scan(&project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := insertEventDecision(tx, eventID, project, d, e.now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

// pruneEvents forgets a project's oldest events beyond the allowance. It is
// tidying only.
func (e *Engine) pruneEvents(project string) {
	unlock := e.writeLock()
	defer unlock()
	for _, p := range []struct {
		where string
		keep  int
	}{{"verified = 0", keepUnverified}, {"verified = 1", keepEvents}} {
		q := `DELETE FROM event_log WHERE project = ? AND ` + p.where + ` AND id NOT IN
			(SELECT id FROM event_log WHERE project = ? AND ` + p.where + ` ORDER BY at DESC, id DESC LIMIT ?)`
		if _, err := e.db.Exec(q, project, project, p.keep); err != nil {
			e.log.Warn("forgetting old events", "project", project, "err", err)
			return
		}
	}
	if _, err := e.db.Exec(`DELETE FROM event_decisions WHERE project = ? AND event_id NOT IN (SELECT id FROM event_log WHERE project = ?)`, project, project); err != nil {
		e.log.Warn("forgetting old decisions", "project", project, "err", err)
	}
}

// EventFilter says which events a list returns.
type EventFilter struct {
	Projects []string // only these projects (nil: all); an empty non-nil list returns nothing
	Project  string
	Source   string
	Decision string // events with at least one decision of this kind
	Verified string // "yes", "no" or ""
	Ref      string
	Since    int64 // Unix ms
	Before   int64 // events older than this time (paging)
	Limit    int
}

// EventLog lists events newest first, each with its decisions.
func (e *Engine) EventLog(f EventFilter) ([]EventRecord, error) {
	if !e.knows(schemaEvents) {
		return []EventRecord{}, nil
	}
	if f.Projects != nil && len(f.Projects) == 0 {
		return []EventRecord{}, nil
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var where []string
	var args []any
	add := func(cond string, a ...any) { where = append(where, cond); args = append(args, a...) }
	if f.Project != "" {
		add("project = ?", f.Project)
	}
	if f.Projects != nil {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(f.Projects)), ",")
		a := make([]any, len(f.Projects))
		for i, p := range f.Projects {
			a[i] = p
		}
		add("project IN ("+ph+")", a...)
	}
	if f.Source != "" {
		add("source = ?", f.Source)
	}
	switch f.Verified {
	case "yes":
		add("verified = 1")
	case "no":
		add("verified = 0")
	}
	if f.Ref != "" {
		add("ref = ?", f.Ref)
	}
	if f.Since > 0 {
		add("at >= ?", f.Since)
	}
	if f.Before > 0 {
		add("at < ?", f.Before)
	}
	if f.Decision != "" {
		add("id IN (SELECT event_id FROM event_decisions WHERE decision = ?)", f.Decision)
	}
	q := `SELECT id, project, at, source, source_name, verified, summary, ref, sha, dedupe_key, payload, replay, replay_of, actor FROM event_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY at DESC, id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	out := []EventRecord{}
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, ev)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Decisions, err = e.eventDecisions(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EventLogEntry returns one event with its decisions.
func (e *Engine) EventLogEntry(id string) (*EventRecord, error) {
	if !e.knows(schemaEvents) {
		return nil, ErrNotFound
	}
	rows, err := e.db.Query(`SELECT id, project, at, source, source_name, verified, summary, ref, sha, dedupe_key, payload, replay, replay_of, actor FROM event_log WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, fmt.Errorf("%w: event %s", ErrNotFound, id)
	}
	ev, err := scanEvent(rows)
	if err != nil {
		return nil, err
	}
	rows.Close()
	if ev.Decisions, err = e.eventDecisions(id); err != nil {
		return nil, err
	}
	return &ev, nil
}

func scanEvent(rows *sql.Rows) (EventRecord, error) {
	var ev EventRecord
	var verified int
	var payload, plan string
	if err := rows.Scan(&ev.ID, &ev.Project, &ev.At, &ev.Source, &ev.SourceName, &verified, &ev.Summary, &ev.Ref, &ev.SHA, &ev.DedupeKey, &payload, &plan, &ev.ReplayOf, &ev.Actor); err != nil {
		return ev, err
	}
	ev.Verified = verified == 1
	if payload != "" {
		ev.Payload = json.RawMessage(payload)
	}
	if plan != "" {
		var p ReplayPlan
		if json.Unmarshal([]byte(plan), &p) == nil {
			ev.Replay = &p
		}
	}
	return ev, nil
}

func (e *Engine) eventDecisions(id string) ([]EventDecision, error) {
	rows, err := e.db.Query(`SELECT id, at, trigger_name, decision, reason, run_id, run_number FROM event_decisions WHERE event_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventDecision{}
	for rows.Next() {
		var d EventDecision
		d.EventID = id
		if err := rows.Scan(&d.ID, &d.At, &d.Trigger, &d.Decision, &d.Reason, &d.RunID, &d.RunNumber); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- what creates events ----

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// eventOfPush describes a push or pull-request decision as an event.
func eventOfPush(project string, d PushDecision) (EventRecord, EventDecision) {
	src, summary, trig := SourcePush, "", "push"
	switch {
	case strings.HasPrefix(d.Ref, "refs/pull/"):
		src, trig = SourcePullRequest, "pull_request"
		n := strings.TrimSuffix(strings.TrimPrefix(d.Ref, "refs/pull/"), "/head")
		summary = fmt.Sprintf("Pull request #%s: head moved to %s", n, shortSHA(d.SHA))
	case strings.HasPrefix(d.Ref, "refs/tags/"):
		summary = fmt.Sprintf("Tag %s at %s", strings.TrimPrefix(d.Ref, "refs/tags/"), shortSHA(d.SHA))
	default:
		summary = fmt.Sprintf("Push to %s at %s", strings.TrimPrefix(d.Ref, "refs/heads/"), shortSHA(d.SHA))
	}
	ev := EventRecord{Project: project, Source: src, Verified: true, Summary: summary, Ref: d.Ref, SHA: d.SHA,
		Replay: &ReplayPlan{Ref: d.Ref, SHA: d.SHA}}
	dec := EventDecision{Trigger: trig, Reason: d.Detail, RunID: d.RunID}
	switch d.Outcome {
	case "started":
		dec.Decision = DecStarted
	case "error":
		dec.Decision = DecError
	case "waiting":
		dec.Decision = DecWaiting
	default: // ignored, skipped_paths
		dec.Decision = DecFiltered
	}
	return ev, dec
}

// EventLink is the event log's part of a new run (NewRun embeds it).
type EventLink struct {
	// EventID names an event already recorded for this run (an event
	// source's, a run-result trigger's): the start is added to it as a
	// decision instead of making a second event. Empty: CreateRun records the
	// event itself.
	EventID string
	// ReplayOf, for a run made by replaying an event, names that event; the
	// run's own event points back to it.
	ReplayOf string
}

// supersededEvent notes on the event that started the run o is about that a
// newer one took its place (a no-op before schema 42).
func (o *cmd) supersededEvent(reason string) error {
	if !o.e.knows(schemaEvents) {
		return nil
	}
	return noteSuperseded(o.tx, o.runID, reason, o.now)
}

// startEvent is the event and decision for a run that CreateRun made, for
// everything but a push (a push's decision is written with it, in
// insertDecision's place).
func (c *cmd) startEvent(nr *NewRun, number int, goes bool, laneNote string, superseded []int, draft bool) error {
	if draft || !c.e.knows(schemaEvents) {
		return nil // a draft run is pipeline text that is not in Git: no trigger judged it
	}
	t := nr.Trigger
	ev := EventRecord{Project: nr.Project, Verified: true, Ref: t.Ref, SHA: t.SHA, Actor: t.Actor}
	trig := t.Event
	switch {
	case nr.Push != nil && strings.HasPrefix(nr.Push.Ref, "refs/pull/"):
		ev.Source, trig = SourcePullRequest, "pull_request"
	case nr.Push != nil:
		ev.Source, trig = SourcePush, "push"
	case nr.Fire != nil:
		ev.Source, ev.SourceName, trig = SourceSchedule, nr.Fire.Key, "schedule"
		ev.Summary = "Schedule fired: " + nr.Fire.Key
	case t.Event == "webhook":
		ev.Source, ev.SourceName, trig = SourceWebhook, t.Webhook, "webhook:"+t.Webhook
		ev.Summary = "Webhook call: " + t.Webhook
		if nr.Delivery != nil {
			ev.DedupeKey = nr.Delivery.Key
		}
	default:
		ev.Source, trig = SourceManual, "manual"
		ev.Summary = "Started by hand"
		if t.Actor != "" {
			ev.Summary += " by " + t.Actor
		}
		if c.st.Rerun != nil {
			ev.Summary = fmt.Sprintf("Re-run of run #%d", c.st.Rerun.OfNumber)
		}
	}
	if nr.Push != nil {
		pushEv, _ := eventOfPush(nr.Project, PushDecision{Ref: nr.Push.Ref, SHA: nr.Push.New})
		ev.Summary = pushEv.Summary
	}
	ev.Replay = &ReplayPlan{Ref: refOrSHA(t), SHA: t.SHA, Inputs: t.Inputs}
	ev.ID = nr.EventID
	if nr.ReplayOf != "" {
		ev.ReplayOf = nr.ReplayOf
		ev.Summary = "Replay of event " + nr.ReplayOf
		if t.Actor != "" {
			ev.Summary += " by " + t.Actor
		}
	}
	dec := EventDecision{Trigger: trig, Decision: DecStarted, RunID: c.runID, RunNumber: number}
	dec.Reason = fmt.Sprintf("started run #%d", number)
	switch {
	case strings.HasPrefix(laneNote, "skipped"):
		dec.Decision, dec.Reason = DecSkipped, laneNote
	case !goes:
		dec.Decision, dec.Reason = DecHeld, fmt.Sprintf("run #%d was created and waits: %s", number, strings.TrimPrefix(laneNote, "held: "))
		if laneNote == "" {
			dec.Reason = fmt.Sprintf("run #%d was created and waits for room", number)
		}
	case laneNote != "":
		dec.Reason += " (" + laneNote + ")"
	}
	if len(superseded) > 0 {
		nums := make([]string, len(superseded))
		for i, n := range superseded {
			nums[i] = fmt.Sprintf("#%d", n)
		}
		dec.Reason += ", superseding " + strings.Join(nums, ", ")
	}
	if nr.Note != "" {
		dec.Reason += "; " + nr.Note
	}
	if nr.EventID != "" {
		// An event that was recorded before the run (a source that filtered,
		// deduplicated or held it) gets the decision; it is not made twice.
		return insertEventDecision(c.tx, nr.EventID, nr.Project, dec, c.now)
	}
	_, err := insertEventTx(c.tx, ev, []EventDecision{dec}, c.now)
	return err
}

func refOrSHA(t Trigger) string {
	if t.Ref != "" {
		return t.Ref
	}
	return t.SHA
}

// recordStart writes what a new run's creation records about what started it:
// its event, and for a push or pull request the decision "Recent pushes"
// shows (which says what the run superseded and what its lane did).
func (c *cmd) recordStart(nr *NewRun, number int, runID string, goes bool, laneNote string, superseded []int, draft bool) error {
	if err := c.startEvent(nr, number, goes, laneNote, superseded, draft); err != nil {
		return err
	}
	if nr.Push == nil {
		return nil
	}
	detail := fmt.Sprintf("started run #%d", number)
	if len(superseded) > 0 {
		nums := make([]string, len(superseded))
		for i, n := range superseded {
			nums[i] = "#" + strconv.Itoa(n)
		}
		detail += ", superseding " + strings.Join(nums, ", ")
	}
	return insertDecision(c.tx, nr.Project, PushDecision{At: c.now, Ref: nr.Push.Ref, SHA: nr.Push.New,
		Outcome: "started", Detail: strings.TrimSpace(detail + " " + nr.Note + laneDetail(laneNote)), RunID: runID})
}
