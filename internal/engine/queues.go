package engine

// Queues and limits (schema 37): how many runs of a kind go at once, and in
// what order the others wait. A run over a limit is created held: none of its
// steps is queued, and run.held says why. When a run ends, the held runs that
// now fit are let go (run.admitted), the most urgent first: a run an operator
// bumped, then by priority (manual, scheduled and ordinary pushes before pull
// requests, automation last), then the oldest. Running steps are never
// stopped for this, and pushes to protected branches are never held.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Classes of runs, for limits and order.
const (
	ClassProtected   = "push_protected"
	ClassPush        = "push"
	ClassPullRequest = "pull_request"
	ClassSchedule    = "schedule"
	ClassManual      = "manual"
)

// Codes of the reasons a run is held, beside the sentence.
const (
	HoldAutomationLimit = "automation_limit"
	HoldClassLimit      = "kind_limit"
	HoldPipelineLimit   = "pipeline_limit"
	HoldProjectLimit    = "project_limit"
	HoldByOperator      = "held_by_operator"
)

func (t EventType) isRunLevel() bool { return t.isLane() || t.isQueue() || t.isTrust() }

func (t EventType) isQueue() bool { return t == RunHeld || t == RunAdmitted || t == RunBumped }

// applyQueue folds run.held, run.admitted and run.bumped.
func (st *RunState) applyQueue(ev Event) {
	q := st.Queue
	if q == nil {
		st.notUnderstood("a %s event for a run with no queue", ev.Type)
		return
	}
	p := ev.Data
	switch ev.Type {
	case RunHeld:
		if !q.Held {
			q.Since = ev.At
		}
		q.Held, q.Manual = true, p.Queue != nil && p.Queue.Manual
		if p.Queue != nil {
			q.Code, q.Reason = p.Queue.Code, p.Queue.Reason
		}
	case RunAdmitted:
		if q.Held && ev.At > q.Since {
			st.Excluded += ev.At - q.Since // the timeout does not run while a run waits in the queue
		}
		q.Held, q.Manual, q.Code, q.Reason = false, false, "", ""
	case RunBumped:
		if p.Reason != "release" {
			q.Bumped = ev.At
		}
		q.Manual, q.Code, q.Reason = false, "", ""
	}
}

// classify says which kind of run this is, and its priority.
func classify(trig Trigger, protected bool) (class string, priority int) {
	switch trig.Event {
	case pipeline.EventPush:
		if protected || trig.Tag != "" {
			return ClassProtected, 30
		}
		return ClassPush, 10
	case pipeline.EventPullRequest:
		return ClassPullRequest, 5
	case pipeline.EventSchedule:
		return ClassSchedule, 10
	}
	return ClassManual, 20
}

type queueRow struct {
	run, pipeline, class string
	automation           bool
	number               int
}

// activeRuns are the runs of a project that count against its limits.
func (c *cmd) activeRuns(project, except string) ([]queueRow, error) {
	rows, err := c.tx.Query(`SELECT q.run_id, q.pipeline, q.class, q.author_class, r.number FROM run_queue q JOIN runs r ON r.id = q.run_id
		WHERE q.project = ? AND q.state = 'active' AND q.run_id <> ? ORDER BY r.number`, project, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []queueRow
	for rows.Next() {
		var q queueRow
		var author string
		if err := rows.Scan(&q.run, &q.pipeline, &q.class, &author, &q.number); err != nil {
			return nil, err
		}
		q.automation = author == "automation"
		out = append(out, q)
	}
	return out, rows.Err()
}

// limitBlock says which limit keeps a run of this class from going, with the
// runs that hold the room, or "" if none does.
func limitBlock(set QueueSettings, active []queueRow, class, pipe string, automation bool) (code, reason string, behind []int) {
	if class == ClassProtected {
		return "", "", nil
	}
	count := func(keep func(queueRow) bool) (n int, nums []int) {
		for _, a := range active {
			if keep(a) {
				n++
				nums = append(nums, a.number)
			}
		}
		return
	}
	perKind := map[string]int{ClassPullRequest: limit(set.PullRequestRuns, DefaultPullRequestRuns), ClassPush: limit(set.PushRuns, 0),
		ClassSchedule: limit(set.ScheduleRuns, 0), ClassManual: limit(set.ManualRuns, 0)}
	kinds := map[string]string{ClassPullRequest: "pull-request", ClassPush: "branch push", ClassSchedule: "scheduled", ClassManual: "manual"}
	checks := []struct {
		code, what string
		max        int
		keep       func(queueRow) bool
	}{
		{HoldAutomationLimit, "runs started by automation accounts", limit(set.AutomationRuns, DefaultAutomationRuns), func(a queueRow) bool { return a.automation }},
		{HoldClassLimit, kinds[class] + " runs", perKind[class], func(a queueRow) bool { return a.class == class }},
		{HoldPipelineLimit, "runs of the pipeline " + pipe, limit(set.PipelineRuns, 0), func(a queueRow) bool { return a.pipeline == pipe }},
		{HoldProjectLimit, "runs", limit(set.ProjectRuns, DefaultProjectRuns), func(queueRow) bool { return true }},
	}
	for _, k := range checks {
		// A person starting a run by hand is not held by the project's total.
		if k.max == 0 || (k.code == HoldAutomationLimit && !automation) || (k.code == HoldProjectLimit && class == ClassManual) {
			continue
		}
		if n, nums := count(k.keep); n >= k.max {
			return k.code, fmt.Sprintf("waiting for room: this project allows %d %s at once, and %s %s going (runs %s)", k.max, k.what, numberWord(n), isAre(n), numberRanges(nums)), nums
		}
	}
	return "", "", nil
}

func numberWord(n int) string { return fmt.Sprint(n) }
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// admitNew decides, for a run being created, whether it is held. It runs in
// the creating transaction, after the project's counter row is taken.
func (c *cmd) admitNew(nr *NewRun, draft bool) (held bool, err error) {
	q := c.st.Queue
	if q == nil || draft || nr.Trigger.Event == pipeline.EventRollback || !c.e.knows(schemaQueues) {
		return false, nil
	}
	active, err := c.activeRuns(c.st.Project, c.runID)
	if err != nil {
		return false, err
	}
	code, reason, _ := limitBlock(c.e.queueSettings(c.st.Project), active, q.Class, c.st.Pipeline.Name, q.Automation)
	if code == "" {
		return false, nil
	}
	c.emit(RunHeld, Payload{Queue: &QueueEvent{Code: code, Reason: reason}, Requires: schemaQueues})
	return true, nil
}

// startHeld lets a held run go: it joins its lane, if it has one, and queues
// its steps.
func (c *cmd) startHeld(actor string) error {
	if c.st.Status.Terminal() || !c.st.queueHeld() {
		return nil
	}
	c.emit(RunAdmitted, Payload{Actor: actor, Requires: schemaQueues})
	goes := true
	if l := c.st.Pipeline.Lane; l != nil && c.st.Draft == nil && c.e.knows(schemaFlow) {
		var err error
		if goes, _, err = c.joinLane(c.st.Project, l, c.st.Trigger, c.st.Number); err != nil {
			return err
		}
	}
	if goes {
		c.advance()
	}
	return nil
}

// admitWaiting lets go the held runs of a project that now fit, most urgent
// first. The project's counter row is already taken.
func (c *cmd) admitWaiting(project string) error {
	for {
		cands, err := c.heldCandidates(project)
		if err != nil {
			return err
		}
		active, err := c.activeRuns(project, "")
		if err != nil {
			return err
		}
		started := false
		for _, x := range cands {
			if code, _, _ := limitBlock(c.e.queueSettings(project), active, x.class, x.pipe, x.author == "automation"); code != "" {
				continue
			}
			called := false
			if err := c.alsoRun(x.id, func(o *cmd) error { called = true; return o.startHeld("") }); err != nil {
				return err
			}
			if !called {
				continue // a run this server does not understand: a newer server starts it
			}
			started = true
			break // the counts changed: look again
		}
		if !started {
			return nil
		}
	}
}

// persistQueues keeps run_queue, which counts and orders the runs of a project,
// and lets held runs go when a run ends.
func (c *cmd) persistQueues() error {
	st := c.st
	if st.Queue == nil {
		return nil
	}
	touched, ended := false, false
	for _, ev := range c.events {
		switch ev.Type {
		case RunCreated, RunHeld, RunAdmitted, RunBumped:
			touched = true
		case RunFinished:
			ended = true
		}
	}
	q := st.Queue
	switch {
	case ended:
		if _, err := c.tx.Exec(`DELETE FROM run_queue WHERE run_id = ?`, st.ID); err != nil {
			return err
		}
		return c.admitWaiting(st.Project)
	case touched:
		state, author := "active", ""
		if q.Held {
			state = "held"
		}
		if q.Automation {
			author = "automation"
		}
		_, err := c.tx.Exec(`INSERT INTO run_queue(run_id, project, pipeline, class, author_class, state, manual, priority, bumped, since, code, reason)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(run_id) DO UPDATE SET state = excluded.state, manual = excluded.manual, bumped = excluded.bumped,
				since = excluded.since, code = excluded.code, reason = excluded.reason`,
			st.ID, st.Project, st.Pipeline.Name, q.Class, author, state, boolInt(q.Manual), q.Priority, q.Bumped, q.Since, q.Code, q.Reason)
		return err
	}
	return nil
}

// recoverQueues lets go held runs that fit but were not started: a limit was
// raised, or the transaction that should have started them was lost.
func (e *Engine) recoverQueues() {
	if !e.knows(schemaQueues) {
		return
	}
	projects, err := e.projectsWithHeldRuns()
	if err != nil {
		e.log.Error("looking for held runs", "err", err)
		return
	}
	for _, p := range projects {
		var id string
		if e.db.QueryRow(`SELECT run_id FROM run_queue WHERE project = ? AND state = 'held' AND manual = 0 LIMIT 1`, p).Scan(&id) != nil {
			continue
		}
		err := e.withRun(id, func(c *cmd) error {
			if err := c.lockLanes(p); err != nil {
				return err
			}
			return c.admitWaiting(p)
		})
		if err != nil {
			e.log.Error("starting held runs", "project", p, "err", err)
		}
	}
}

// Queue actions an operator may take on a waiting run.
const (
	QueueRunNext = "run_next"
	QueueHold    = "hold"
	QueueRelease = "release"
)

// QueueAction moves a run in the queue: run_next puts it at the front (and
// lets it go at once if the limits allow), hold keeps it waiting until
// released, release lets limits decide again. A run that already goes is
// never stopped.
func (e *Engine) QueueAction(runID, action, actor string) error {
	if !e.knows(schemaQueues) {
		return fmt.Errorf("%w: this server does not know queues (schema %d)", ErrConflict, schemaQueues)
	}
	return e.withRun(runID, func(c *cmd) error {
		st := c.st
		if st.Queue == nil || !st.Queue.Held || st.Status.Terminal() {
			return fmt.Errorf("%w: the run is not waiting in a queue: it is already going or over", ErrConflict)
		}
		if err := c.lockLanes(st.Project); err != nil {
			return err
		}
		switch action {
		case QueueRunNext:
			c.emit(RunBumped, Payload{Actor: actor, Requires: schemaQueues})
			return c.admitSelf(actor)
		case QueueHold:
			if st.Queue.Manual {
				return fmt.Errorf("%w: the run is already held", ErrConflict)
			}
			c.emit(RunHeld, Payload{Actor: actor, Requires: schemaQueues, Queue: &QueueEvent{Manual: true, Code: HoldByOperator,
				Reason: "held by " + nonEmpty(actor, "an operator") + ": release it, or choose run next, to let it go"}})
		case QueueRelease:
			if !st.Queue.Manual {
				return fmt.Errorf("%w: the run is not held by a person", ErrConflict)
			}
			c.emit(RunBumped, Payload{Actor: actor, Reason: "release", Requires: schemaQueues})
			return c.admitSelf(actor)
		default:
			return fmt.Errorf("%w: unknown queue action %q (run_next, hold or release)", ErrConflict, action)
		}
		return nil
	})
}

// queueActions are the actions on a run that waits in a queue, added to the
// run's list with whether each is available now and, if not, why.
func (st *RunState) queueActions() []Action {
	waiting := st.Queue != nil && st.Queue.Held && !st.Status.Terminal()
	why := "the run is not waiting in a queue"
	mk := func(name string, ok bool, reason string) Action {
		a := Action{Name: name, Available: waiting && ok, Who: "operator"}
		if !a.Available {
			a.Reason = reason
			if !waiting {
				a.Reason = why
			}
		}
		return a
	}
	manual := waiting && st.Queue.Manual
	return []Action{mk(QueueRunNext, true, ""), mk(QueueHold, !manual, "the run is already held"), mk(QueueRelease, manual, "the run is not held by a person")}
}

// QueueEntry is a run waiting in a project's queue, as Activity shows it.
type QueueEntry struct {
	Run        string `json:"run"`
	Number     int    `json:"number"`
	Project    string `json:"project"`
	Pipeline   string `json:"pipeline"`
	Class      string `json:"class"`
	Position   int    `json:"position"`
	automation bool
	Since      int64    `json:"since"`
	Code       string   `json:"code"`
	Reason     string   `json:"reason"`
	Behind     []int    `json:"behind,omitempty"`
	Actions    []Action `json:"actions"`
}

// Queue lists the runs waiting in queues, in the order they will go, with why
// each waits and which runs it waits behind. project limits it to one.
func (e *Engine) Queue(project string) ([]QueueEntry, error) {
	out := []QueueEntry{}
	if !e.knows(schemaQueues) {
		return out, nil
	}
	q := `SELECT q.run_id, r.number, q.project, q.pipeline, q.class, q.author_class, q.manual, q.since, q.code, q.reason
		FROM run_queue q JOIN runs r ON r.id = q.run_id WHERE q.state = 'held'`
	var args []any
	if project != "" {
		q += ` AND q.project = ?`
		args = append(args, project)
	}
	rows, err := e.db.Query(q+` ORDER BY q.project, q.bumped DESC, q.priority DESC, q.since, r.number`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pos := map[string]int{}
	for rows.Next() {
		var x QueueEntry
		var author string
		var manual int
		if err := rows.Scan(&x.Run, &x.Number, &x.Project, &x.Pipeline, &x.Class, &author, &manual, &x.Since, &x.Code, &x.Reason); err != nil {
			return nil, err
		}
		x.automation = author == "automation"
		pos[x.Project]++
		x.Position = pos[x.Project]
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		x := &out[i]
		active, err := e.activeRunsDB(x.Project)
		if err != nil {
			return nil, err
		}
		if x.Code != HoldByOperator {
			code, reason, behind := limitBlock(e.queueSettings(x.Project), active, x.Class, x.Pipeline, x.automation)
			if code != "" {
				x.Code, x.Reason, x.Behind = code, reason, behind
			}
		}
		if x.Position > 1 && x.Code != HoldByOperator {
			x.Reason += fmt.Sprintf("; %d held run%s go first", x.Position-1, pluralS(x.Position-1))
		}
		slices.Sort(x.Behind)
		x.Actions = []Action{}
	}
	return out, nil
}

func (e *Engine) activeRunsDB(project string) ([]queueRow, error) {
	var out []queueRow
	rows, err := e.db.Query(`SELECT q.run_id, q.pipeline, q.class, q.author_class, r.number FROM run_queue q JOIN runs r ON r.id = q.run_id
		WHERE q.project = ? AND q.state = 'active'`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var q queueRow
		var a string
		if err := rows.Scan(&q.run, &q.pipeline, &q.class, &a, &q.number); err != nil {
			return nil, err
		}
		q.automation = a == "automation"
		out = append(out, q)
	}
	return out, rows.Err()
}

// queueOf is what run.created records for the queue (nil before schema 37).
func (e *Engine) queueOf(nr *NewRun, trig Trigger) *QueueEvent {
	if !e.knows(schemaQueues) {
		return nil
	}
	class, prio := classify(trig, nr.Protected)
	if nr.Automation && class != ClassProtected {
		prio = 0
	}
	return &QueueEvent{Class: class, Automation: nr.Automation, Priority: prio}
}

// supersedes says whether a new run cancels the older runs of its ref: the
// pipeline asks for it, or the run is for a pull request and neither the
// pipeline (supersede: false) nor the queues setting says otherwise.
func (e *Engine) supersedes(nr *NewRun, draft *Draft) bool {
	if nr.Pipeline.Supersede {
		return true
	}
	return draft == nil && nr.Trigger.Event == pipeline.EventPullRequest && e.knows(schemaQueues) && !nr.Pipeline.SupersedeOff &&
		e.queueSettings(nr.Project).supersedesPullRequests()
}

// supersededWords is the reason a run gives when a newer one replaced it.
func supersededWords(event string, number int) string {
	if event == pipeline.EventPullRequest {
		return fmt.Sprintf("superseded by run #%d: a newer commit was pushed to the same pull request", number)
	}
	return fmt.Sprintf("superseded by run #%d", number)
}

// QueueInfo is a run's place in the queue as shown with the run.
type QueueInfo struct {
	Class  string `json:"class"`
	Held   bool   `json:"held"`
	Manual bool   `json:"manual,omitempty"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// attachQueue gives each run in the list its queue state.
func (e *Engine) attachQueue(runs []*RunSummary) {
	if !e.knows(schemaQueues) || len(runs) == 0 {
		return
	}
	marks := make([]string, len(runs))
	args := make([]any, len(runs))
	byID := map[string]*RunSummary{}
	for i, r := range runs {
		marks[i], args[i], byID[r.ID] = "?", r.ID, r
	}
	rows, err := e.db.Query(`SELECT run_id, class, state, manual, code, reason FROM run_queue WHERE run_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, state string
		var manual int
		q := &QueueInfo{}
		if rows.Scan(&id, &q.Class, &state, &manual, &q.Code, &q.Reason) != nil {
			return
		}
		q.Held, q.Manual = state == "held", manual != 0
		if !q.Held {
			q.Code, q.Reason = "", ""
		}
		byID[id].Queue = q
	}
	_ = rows.Err() // a list without queue marks is still a list
}

// admitSelf lets this run go now if the limits have room for it.
func (c *cmd) admitSelf(actor string) error {
	active, err := c.activeRuns(c.st.Project, c.runID)
	if err != nil {
		return err
	}
	q := c.st.Queue
	if code, _, _ := limitBlock(c.e.queueSettings(c.st.Project), active, q.Class, c.st.Pipeline.Name, q.Automation); code != "" {
		return nil
	}
	return c.startHeld(actor)
}

// admitOrLane decides what happens to a run being created: held in the queue
// (it does not go, and the note says why), or sent on to its lane.
func (c *cmd) admitOrLane(nr *NewRun, draft bool, trig Trigger, number int) (goes bool, note string, err error) {
	held, err := c.admitNew(nr, draft)
	if err != nil || held {
		return false, "held: " + c.st.Queue.Reason, err
	}
	return c.takeLane(nr, draft, trig, number)
}

type heldCandidate struct{ id, pipe, class, author string }

// heldCandidates are a project's held runs that limits may let go, most
// urgent first.
func (c *cmd) heldCandidates(project string) ([]heldCandidate, error) {
	rows, err := c.tx.Query(`SELECT run_id, pipeline, class, author_class FROM run_queue WHERE project = ? AND state = 'held' AND manual = 0
		ORDER BY bumped DESC, priority DESC, since, run_id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []heldCandidate
	for rows.Next() {
		var x heldCandidate
		if err := rows.Scan(&x.id, &x.pipe, &x.class, &x.author); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (e *Engine) projectsWithHeldRuns() ([]string, error) {
	rows, err := e.db.Query(`SELECT DISTINCT project FROM run_queue WHERE state = 'held' AND manual = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// persistQueueTables keeps step_waits and run_queue.
func (c *cmd) persistQueueTables(changed []string) error {
	if err := c.persistWaits(changed); err != nil {
		return err
	}
	return c.persistQueues()
}
