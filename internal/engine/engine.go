// Package engine runs pipelines durably.
//
// Every change to a run is an event appended to the database. A run's state
// is rebuilt from its events, so the engine can stop at any moment (crash,
// upgrade, power cut) and carry on from exactly where it was. Steps are handed
// to runners on leases that the runner must keep renewing; a step whose
// runner disappears is queued again.
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"slipway/internal/hub"
	"slipway/internal/ids"
	"slipway/internal/pipeline"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrLeaseLost = errors.New("the lease is no longer valid")
	ErrClosed    = errors.New("the engine is shutting down")
)

// Options configure an Engine.
type Options struct {
	Path     string        // SQLite database file
	LeaseTTL time.Duration // how long a runner may go silent; default 30s
	MaxLost  int           // times a step is requeued after losing its runner; default 3
	Hub      *hub.Hub      // live updates; optional
	Logger   *slog.Logger
	Now      func() time.Time // for tests
}

// Engine is safe for concurrent use.
type Engine struct {
	db      *sql.DB
	hub     *hub.Hub
	log     *slog.Logger
	ttl     time.Duration
	maxLost int
	now     func() time.Time

	mu     sync.Mutex // serialises every write
	leases map[string]*lease

	wakeMu sync.Mutex
	wake   chan struct{}

	stop      chan struct{}
	reaperOut chan struct{}
	closeOnce sync.Once
}

type lease struct {
	id, runID, stepID string
	attempt           int
	runner            string
	expires           time.Time
	cancel            bool
	logSeq            int
}

// RunnerInfo identifies a runner asking for work.
type RunnerInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Executor string `json:"executor"`
	Version  string `json:"version"`
}

// Job is everything a runner needs to execute one step attempt.
type Job struct {
	LeaseID   string            `json:"lease_id"`
	LeaseTTL  pipeline.Duration `json:"lease_ttl"`
	RunID     string            `json:"run_id"`
	Project   string            `json:"project"`
	RunNumber int               `json:"run_number"`
	Pipeline  string            `json:"pipeline"`
	StepID    string            `json:"step_id"`
	Attempt   int               `json:"attempt"`
	Image     string            `json:"image"`
	Script    string            `json:"script"`
	Env       map[string]string `json:"env"`
	Timeout   pipeline.Duration `json:"timeout"`
	Source    Source            `json:"source"`
	SHA       string            `json:"sha,omitempty"`
}

// Result is how a step attempt ended, as reported by its runner.
type Result struct {
	Outcome  string            `json:"outcome"` // succeeded, failed or canceled
	ExitCode *int              `json:"exit_code,omitempty"`
	Error    string            `json:"error,omitempty"`
	Outputs  map[string]string `json:"outputs,omitempty"`
}

// LogLine is one line of a step's output.
type LogLine struct {
	Seq    int    `json:"seq"`
	At     int64  `json:"at"`
	Stream string `json:"stream"` // stdout, stderr or system
	Text   string `json:"text"`
}

// Open opens the database, recovers any steps that were in flight, and starts
// watching leases.
func Open(o Options) (*Engine, error) {
	if o.LeaseTTL == 0 {
		o.LeaseTTL = 30 * time.Second
	}
	if o.MaxLost == 0 {
		o.MaxLost = 3
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Hub == nil {
		o.Hub = hub.New()
	}
	db, err := openDB(o.Path)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		db: db, hub: o.Hub, log: o.Logger, ttl: o.LeaseTTL, maxLost: o.MaxLost, now: o.Now,
		leases:    map[string]*lease{},
		wake:      make(chan struct{}),
		stop:      make(chan struct{}),
		reaperOut: make(chan struct{}),
	}
	if err := e.recoverLeases(); err != nil {
		db.Close()
		return nil, err
	}
	go e.reaper()
	return e, nil
}

// Close stops the engine. Runs in flight stay recorded and continue when it
// is opened again.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		close(e.stop)
		<-e.reaperOut
		e.notify()
	})
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.db.Close()
}

// Hub returns the live update hub.
func (e *Engine) Hub() *hub.Hub { return e.hub }

// DB exposes the database to packages that keep small tables of their own.
func (e *Engine) DB() *sql.DB { return e.db }

// recoverLeases gives every step that was in flight when the engine stopped a
// fresh lease period. If its runner is still alive it carries on; if not, the
// lease expires and the step is queued again.
func (e *Engine) recoverLeases() error {
	rows, err := e.db.Query(`SELECT s.run_id, s.step_id, s.attempt, s.lease_id, s.runner, r.cancel_requested
		FROM steps s JOIN runs r ON r.id = s.run_id WHERE s.status IN ('leased', 'running')`)
	if err != nil {
		return err
	}
	var found []*lease
	for rows.Next() {
		l := &lease{expires: e.now().Add(e.ttl)}
		var cancel int
		if err := rows.Scan(&l.runID, &l.stepID, &l.attempt, &l.id, &l.runner, &cancel); err != nil {
			rows.Close()
			return err
		}
		l.cancel = cancel != 0
		found = append(found, l)
	}
	rows.Close()
	for _, l := range found {
		if err := e.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM logs WHERE run_id = ? AND step_id = ? AND attempt = ?`,
			l.runID, l.stepID, l.attempt).Scan(&l.logSeq); err != nil {
			return err
		}
		e.leases[l.id] = l
	}
	if len(found) > 0 {
		e.log.Info("recovered steps that were in flight", "count", len(found))
	}
	return nil
}

// ---- commands ----

// NewRun describes a run to create.
type NewRun struct {
	Project  string
	Pipeline *pipeline.Pipeline
	Trigger  Trigger
	Source   Source
}

// CreateRun records a new run and queues the steps that can start.
func (e *Engine) CreateRun(nr NewRun) (*RunSummary, error) {
	if nr.Project == "" || nr.Pipeline == nil || len(nr.Pipeline.Steps) == 0 {
		return nil, fmt.Errorf("%w: a run needs a project and a pipeline with steps", ErrConflict)
	}
	if nr.Trigger.Event == "" {
		nr.Trigger.Event = "manual"
	}
	runID := ids.New("run")
	e.mu.Lock()
	defer e.mu.Unlock()
	var number int
	if err := e.db.QueryRow(`SELECT COALESCE(MAX(number), 0) + 1 FROM runs WHERE project = ?`, nr.Project).Scan(&number); err != nil {
		return nil, err
	}
	trig, src := nr.Trigger, nr.Source
	err := e.txLocked(runID, true, func(c *cmd) error {
		c.emit(RunCreated, Payload{Project: nr.Project, Number: number, Pipeline: nr.Pipeline, Trigger: &trig, Source: &src})
		c.advance()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.runSummary(runID)
}

// Cancel asks a run to stop. Steps not yet running are canceled at once;
// running steps are stopped by their runners.
func (e *Engine) Cancel(runID, actor string) error {
	return e.withRun(runID, func(c *cmd) error {
		if c.st.Status.Terminal() {
			return fmt.Errorf("%w: the run has already finished", ErrConflict)
		}
		if c.st.CancelRequested {
			return nil
		}
		c.emit(RunCancelRequested, Payload{Actor: actor})
		c.advance()
		c.after(func() {
			for _, l := range e.leases {
				if l.runID == runID {
					l.cancel = true
				}
			}
		})
		return nil
	})
}

// Resume restarts a failed or canceled run from the steps that did not
// succeed. Steps that succeeded are not run again.
func (e *Engine) Resume(runID, actor string) error {
	return e.withRun(runID, func(c *cmd) error {
		if c.st.Status != RunFailed && c.st.Status != RunCanceled {
			return fmt.Errorf("%w: only a failed or canceled run can be resumed", ErrConflict)
		}
		c.emit(RunResumed, Payload{Actor: actor})
		for _, id := range c.st.Order {
			s := c.st.Steps[id]
			if s.Status == StepFailed || s.Status == StepCanceledS || (s.Status == StepSkippedS && s.SkipKind == "upstream") {
				c.emit(StepReset, Payload{Step: id})
			}
		}
		c.advance()
		return nil
	})
}

// Lease hands the oldest queued step to a runner, waiting up to wait for one
// to become available. It returns nil, nil if there is none. It stops waiting
// when ctx ends, for example because the runner disconnected.
func (e *Engine) Lease(ctx context.Context, r RunnerInfo, wait time.Duration) (*Job, error) {
	deadline := time.Now().Add(wait)
	for {
		ch := e.waitChan()
		select {
		case <-e.stop:
			return nil, ErrClosed
		default:
		}
		e.touchRunner(r)
		job, err := e.tryLease(r)
		if err != nil || job != nil {
			return job, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil
		}
		t := time.NewTimer(left)
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
			return nil, nil
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-e.stop:
			t.Stop()
			return nil, ErrClosed
		}
	}
}

// Release hands back a step that was leased but never started, for example
// because the runner disconnected before receiving it. It is queued again
// at once, as the same attempt.
func (e *Engine) Release(leaseID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.leases[leaseID]
	if l == nil {
		return ErrLeaseLost
	}
	err := e.withRunLocked(l.runID, func(c *cmd) error {
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || s.Status != StepLeasedS {
			return ErrLeaseLost
		}
		if c.st.CancelRequested {
			c.emit(StepCanceled, Payload{Step: l.stepID, Attempt: s.Attempt, Reason: "the run was canceled"})
		} else {
			c.emit(StepQueued, Payload{Step: l.stepID, Attempt: s.Attempt, Reason: "handed back before it started"})
		}
		c.advance()
		return nil
	})
	delete(e.leases, leaseID)
	return err
}

func (e *Engine) tryLease(r RunnerInfo) (*Job, error) {
	rows, err := e.db.Query(`SELECT run_id, step_id, attempt FROM steps WHERE status = 'queued'
		ORDER BY queued_at, run_id, position LIMIT 20`)
	if err != nil {
		return nil, err
	}
	type cand struct {
		run, step string
		attempt   int
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.run, &c.step, &c.attempt); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	for _, cd := range cands {
		var job *Job
		err := e.withRun(cd.run, func(c *cmd) error {
			s := c.st.Steps[cd.step]
			if s == nil || s.Status != StepQueuedS || s.Attempt != cd.attempt || c.st.CancelRequested {
				return nil // someone else got it, or it changed
			}
			leaseID := ids.New("lease")
			c.emit(StepLeased, Payload{Step: cd.step, Attempt: s.Attempt, Lease: leaseID, Runner: r.ID})
			job = c.job(cd.step, leaseID)
			l := &lease{id: leaseID, runID: cd.run, stepID: cd.step, attempt: s.Attempt, runner: r.ID, expires: e.now().Add(e.ttl)}
			c.after(func() { e.leases[leaseID] = l })
			return nil
		})
		if err != nil {
			return nil, err
		}
		if job != nil {
			return job, nil
		}
	}
	return nil, nil
}

// Start records that the runner began executing the step. It reports whether
// the run has been asked to cancel.
func (e *Engine) Start(leaseID string) (cancel bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.leases[leaseID]
	if l == nil {
		return false, ErrLeaseLost
	}
	l.expires = e.now().Add(e.ttl)
	err = e.withRunLocked(l.runID, func(c *cmd) error {
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || !s.Status.Active() {
			c.after(func() { delete(e.leases, leaseID) })
			return ErrLeaseLost
		}
		if s.Status == StepLeasedS {
			c.emit(StepStarted, Payload{Step: l.stepID, Attempt: l.attempt, Lease: leaseID})
		}
		return nil
	})
	if errors.Is(err, ErrLeaseLost) {
		delete(e.leases, leaseID)
	}
	return l.cancel, err
}

// Heartbeat renews a lease. It reports whether the run has been asked to
// cancel, in which case the runner should stop the step.
func (e *Engine) Heartbeat(leaseID string) (cancel bool, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.leases[leaseID]
	if l == nil {
		return false, ErrLeaseLost
	}
	l.expires = e.now().Add(e.ttl)
	return l.cancel, nil
}

// Finish records how a step attempt ended.
func (e *Engine) Finish(leaseID string, res Result) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.leases[leaseID]
	if l == nil {
		return ErrLeaseLost
	}
	err := e.withRunLocked(l.runID, func(c *cmd) error {
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || !s.Status.Active() {
			return ErrLeaseLost
		}
		p := Payload{Step: l.stepID, Attempt: l.attempt, Lease: leaseID, ExitCode: res.ExitCode, Outputs: res.Outputs}
		switch res.Outcome {
		case "succeeded":
			p.Outcome = string(StepSucceeded)
			c.emit(StepFinished, p)
		case "canceled":
			if c.st.CancelRequested {
				p.Reason = "canceled"
				c.emit(StepCanceled, p)
			} else {
				// The runner gave up without being asked, for example
				// because it is shutting down. Treat it like a lost runner.
				c.lose(l.stepID, nonEmpty(res.Error, "the runner stopped the step"))
			}
		default:
			p.Outcome = string(StepFailed)
			p.Reason = nonEmpty(res.Error, "the step failed")
			c.emit(StepFinished, p)
			def := c.st.Pipeline.Step(l.stepID)
			if def != nil && s.Failures <= def.Retry && !c.st.CancelRequested {
				c.emit(StepQueued, Payload{Step: l.stepID, Attempt: s.Attempt + 1, Reason: "retry"})
			}
		}
		c.advance()
		return nil
	})
	if err == nil || errors.Is(err, ErrLeaseLost) {
		delete(e.leases, leaseID)
	}
	return err
}

// Limits on log volume, to protect the database from runaway output.
const (
	MaxLogLineBytes = 64 * 1024
	MaxLogBatch     = 2000
)

// AppendLogs stores output lines for the step held by leaseID. Sending logs
// also renews the lease.
func (e *Engine) AppendLogs(leaseID string, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > MaxLogBatch {
		return fmt.Errorf("%w: at most %d lines per call", ErrConflict, MaxLogBatch)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.leases[leaseID]
	if l == nil {
		return ErrLeaseLost
	}
	l.expires = e.now().Add(e.ttl)
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seq := l.logSeq
	out := make([]LogLine, len(lines))
	for i, ln := range lines {
		seq++
		if len(ln.Text) > MaxLogLineBytes {
			ln.Text = ln.Text[:MaxLogLineBytes] + " …[line truncated]"
		}
		if ln.At == 0 {
			ln.At = e.now().UnixMilli()
		}
		if ln.Stream == "" {
			ln.Stream = "stdout"
		}
		ln.Seq = seq
		out[i] = ln
		if _, err := tx.Exec(`INSERT INTO logs(run_id, step_id, attempt, seq, at, stream, text) VALUES(?,?,?,?,?,?,?)`,
			l.runID, l.stepID, l.attempt, ln.Seq, ln.At, ln.Stream, ln.Text); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	l.logSeq = seq
	e.hub.Publish(hub.Message{Type: "log", RunID: l.runID, Data: LogBatch{Step: l.stepID, Attempt: l.attempt, Lines: out}})
	return nil
}

// LogBatch is the live update sent when log lines arrive.
type LogBatch struct {
	Step    string    `json:"step"`
	Attempt int       `json:"attempt"`
	Lines   []LogLine `json:"lines"`
}

// ---- lease expiry ----

func (e *Engine) reaper() {
	defer close(e.reaperOut)
	interval := e.ttl / 10
	if interval < 50*time.Millisecond {
		interval = 50 * time.Millisecond
	}
	if interval > time.Second {
		interval = time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.reap()
		}
	}
}

func (e *Engine) reap() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	var expired []*lease
	for _, l := range e.leases {
		if now.After(l.expires) {
			expired = append(expired, l)
		}
	}
	for _, l := range expired {
		l := l
		err := e.withRunLocked(l.runID, func(c *cmd) error {
			s := c.st.Steps[l.stepID]
			if s != nil && s.LeaseID == l.id && s.Status.Active() {
				e.log.Warn("runner stopped responding", "run", l.runID, "step", l.stepID, "runner", l.runner)
				c.lose(l.stepID, "the runner stopped responding")
				c.advance()
			}
			c.after(func() { delete(e.leases, l.id) })
			return nil
		})
		if err != nil {
			e.log.Error("expiring a lease", "lease", l.id, "err", err)
		}
	}
}

// ---- the transaction helper ----

type cmd struct {
	e       *Engine
	runID   string
	st      *RunState
	now     int64
	events  []Event
	changed map[string]bool
	queued  bool
	hooks   []func()
}

func (c *cmd) emit(t EventType, p Payload) {
	if c.st == nil {
		c.st = &RunState{}
	}
	ev := Event{RunID: c.runID, Type: t, At: c.now, Data: p}
	c.st.apply(ev)
	c.events = append(c.events, ev)
	if t == RunCreated {
		for _, id := range c.st.Order {
			c.changed[id] = true
		}
	}
	if p.Step != "" {
		c.changed[p.Step] = true
	}
	if t == StepQueued {
		c.queued = true
	}
}

// after runs fn once the transaction has committed, still holding the lock.
func (c *cmd) after(fn func()) { c.hooks = append(c.hooks, fn) }

func (e *Engine) withRun(runID string, fn func(c *cmd) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.withRunLocked(runID, fn)
}

func (e *Engine) withRunLocked(runID string, fn func(c *cmd) error) error {
	return e.txLocked(runID, false, fn)
}

// txLocked runs fn against the run's current state in one transaction.
// Unless create is set, the run must already exist.
func (e *Engine) txLocked(runID string, create bool, fn func(c *cmd) error) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	evs, err := loadEvents(tx, runID)
	if err != nil {
		return err
	}
	c := &cmd{e: e, runID: runID, st: Fold(evs), now: e.now().UnixMilli(), changed: map[string]bool{}}
	if c.st == nil {
		if !create {
			return fmt.Errorf("%w: run %s", ErrNotFound, runID)
		}
		c.st = &RunState{}
	}
	if err := fn(c); err != nil {
		if len(c.hooks) > 0 && errors.Is(err, ErrLeaseLost) {
			for _, h := range c.hooks {
				h()
			}
		}
		return err
	}
	if len(c.events) > 0 {
		if err := c.persist(tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	for _, h := range c.hooks {
		h()
	}
	if len(c.events) > 0 {
		e.publishChanges(runID, c.changed)
		if c.queued {
			e.notify()
		}
	}
	return nil
}

func loadEvents(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, runID string) ([]Event, error) {
	rows, err := q.Query(`SELECT seq, type, at, data FROM events WHERE run_id = ? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evs []Event
	for rows.Next() {
		ev := Event{RunID: runID}
		var data string
		if err := rows.Scan(&ev.Seq, &ev.Type, &ev.At, &data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &ev.Data); err != nil {
			return nil, fmt.Errorf("event %d: %w", ev.Seq, err)
		}
		evs = append(evs, ev)
	}
	return evs, rows.Err()
}

func (c *cmd) persist(tx *sql.Tx) error {
	for i := range c.events {
		ev := &c.events[i]
		data, err := json.Marshal(ev.Data)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO events(run_id, type, at, data) VALUES(?,?,?,?)`, ev.RunID, ev.Type, ev.At, string(data))
		if err != nil {
			return err
		}
		if ev.Seq, err = res.LastInsertId(); err != nil {
			return err
		}
		c.st.LastSeq = ev.Seq
	}
	st := c.st
	t := st.Trigger
	if _, err := tx.Exec(`INSERT INTO runs(id, project, number, pipeline, status, cancel_requested, event, ref, branch, tag, sha,
			actor, message, author, created_at, started_at, finished_at, last_seq)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, cancel_requested = excluded.cancel_requested,
			started_at = excluded.started_at, finished_at = excluded.finished_at, last_seq = excluded.last_seq`,
		st.ID, st.Project, st.Number, st.Pipeline.Name, st.Status, boolInt(st.CancelRequested), t.Event, t.Ref, t.Branch, t.Tag, t.SHA,
		t.Actor, t.Message, t.Author, st.CreatedAt, st.StartedAt, st.FinishedAt, st.LastSeq); err != nil {
		return err
	}
	for id := range c.changed {
		s := st.Steps[id]
		outputs, _ := json.Marshal(s.Outputs)
		if s.Outputs == nil {
			outputs = []byte("{}")
		}
		if _, err := tx.Exec(`INSERT INTO steps(run_id, step_id, position, status, attempt, queued_at, started_at, finished_at,
				exit_code, error, skip_kind, runner, lease_id, outputs)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(run_id, step_id) DO UPDATE SET status = excluded.status, attempt = excluded.attempt,
				queued_at = excluded.queued_at, started_at = excluded.started_at, finished_at = excluded.finished_at,
				exit_code = excluded.exit_code, error = excluded.error, skip_kind = excluded.skip_kind,
				runner = excluded.runner, lease_id = excluded.lease_id, outputs = excluded.outputs`,
			st.ID, id, s.Position, s.Status, s.Attempt, s.QueuedAt, s.StartedAt, s.FinishedAt,
			s.ExitCode, s.Error, s.SkipKind, s.Runner, s.LeaseID, string(outputs)); err != nil {
			return err
		}
	}
	return nil
}

// advance moves the run forward: queue steps whose needs are met, skip steps
// that cannot run, cancel what remains if a cancel was asked for, and finish
// the run when nothing is left to do.
func (c *cmd) advance() {
	st := c.st
	if st.Status.Terminal() {
		return
	}
	ctx := st.Trigger.Context()
	for changed := true; changed; {
		changed = false
		for _, id := range st.Order {
			s := st.Steps[id]
			if st.CancelRequested && (s.Status == StepPending || s.Status == StepQueuedS) {
				c.emit(StepCanceled, Payload{Step: id, Reason: "the run was canceled"})
				changed = true
				continue
			}
			if s.Status != StepPending {
				continue
			}
			def := st.Pipeline.Step(id)
			ready, blocked := true, ""
			for _, n := range def.Needs {
				switch dep := st.Steps[n]; dep.Status {
				case StepSucceeded:
				case StepFailed, StepSkippedS, StepCanceledS:
					if blocked == "" {
						blocked = fmt.Sprintf("%s %s", n, dep.Status)
					}
				default:
					ready = false
				}
			}
			switch {
			case blocked != "":
				c.emit(StepSkipped, Payload{Step: id, SkipKind: "upstream", Reason: "not run because " + blocked})
				changed = true
			case !ready:
			case !def.When.Match(ctx):
				c.emit(StepSkipped, Payload{Step: id, SkipKind: "condition", Reason: "its when condition did not match"})
				changed = true
			default:
				c.emit(StepQueued, Payload{Step: id, Attempt: s.Attempt + 1})
				changed = true
			}
		}
	}
	failed, canceled := false, false
	for _, id := range st.Order {
		switch st.Steps[id].Status {
		case StepSucceeded, StepSkippedS:
		case StepFailed:
			failed = true
		case StepCanceledS:
			canceled = true
		default:
			return // something is still pending or running
		}
	}
	status := RunSucceeded
	switch {
	case failed:
		status = RunFailed
	case canceled:
		status = RunCanceled
	}
	c.emit(RunFinished, Payload{Status: string(status)})
}

// lose records that a step's runner disappeared, and queues it again unless
// it has been lost too often or the run is being canceled.
func (c *cmd) lose(stepID, reason string) {
	s := c.st.Steps[stepID]
	c.emit(StepLost, Payload{Step: stepID, Attempt: s.Attempt, Reason: reason})
	switch {
	case c.st.CancelRequested:
		c.emit(StepCanceled, Payload{Step: stepID, Attempt: s.Attempt, Reason: "the run was canceled"})
	case s.Lost <= c.e.maxLost:
		c.emit(StepQueued, Payload{Step: stepID, Attempt: s.Attempt + 1, Reason: "requeued after: " + reason})
	default:
		c.emit(StepFinished, Payload{Step: stepID, Attempt: s.Attempt, Outcome: string(StepFailed),
			Reason: fmt.Sprintf("%s (gave up after %d tries)", reason, s.Lost)})
	}
}

// job builds what the runner needs, including the outputs of needed steps.
func (c *cmd) job(stepID, leaseID string) *Job {
	st := c.st
	def := st.Pipeline.Step(stepID)
	s := st.Steps[stepID]
	t := st.Trigger
	env := map[string]string{}
	for k, v := range def.Env {
		env[k] = v
	}
	for _, n := range def.Needs {
		for k, v := range st.Steps[n].Outputs {
			env["SLIPWAY_NEEDS_"+envName(n)+"_"+envName(k)] = v
		}
	}
	for k, v := range map[string]string{
		"CI": "true", "SLIPWAY": "true",
		"SLIPWAY_RUN_ID": st.ID, "SLIPWAY_RUN_NUMBER": strconv.Itoa(st.Number),
		"SLIPWAY_PROJECT": st.Project, "SLIPWAY_PIPELINE": st.Pipeline.Name,
		"SLIPWAY_STEP": stepID, "SLIPWAY_ATTEMPT": strconv.Itoa(s.Attempt),
		"SLIPWAY_EVENT": t.Event, "SLIPWAY_REF": t.Ref, "SLIPWAY_BRANCH": t.Branch,
		"SLIPWAY_TAG": t.Tag, "SLIPWAY_COMMIT": t.SHA,
	} {
		env[k] = v
	}
	return &Job{
		LeaseID: leaseID, LeaseTTL: pipeline.Duration(c.e.ttl),
		RunID: st.ID, Project: st.Project, RunNumber: st.Number, Pipeline: st.Pipeline.Name,
		StepID: stepID, Attempt: s.Attempt, Image: def.Image, Script: def.Run, Env: env,
		Timeout: def.Timeout, Source: st.Source, SHA: t.SHA,
	}
}

func envName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, s)
}

// ---- live updates and wake-ups ----

func (e *Engine) publishChanges(runID string, steps map[string]bool) {
	if sum, err := e.runSummary(runID); err == nil {
		e.hub.Publish(hub.Message{Type: "run", RunID: runID, Data: sum})
	}
	for id := range steps {
		if v, err := e.stepView(runID, id); err == nil {
			e.hub.Publish(hub.Message{Type: "step", RunID: runID, Data: v})
		}
	}
}

func (e *Engine) waitChan() <-chan struct{} {
	e.wakeMu.Lock()
	defer e.wakeMu.Unlock()
	return e.wake
}

func (e *Engine) notify() {
	e.wakeMu.Lock()
	close(e.wake)
	e.wake = make(chan struct{})
	e.wakeMu.Unlock()
}

func (e *Engine) touchRunner(r RunnerInfo) {
	if r.ID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.db.Exec(`INSERT INTO runners(id, name, executor, version, last_seen) VALUES(?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, executor = excluded.executor,
			version = excluded.version, last_seen = excluded.last_seen`,
		r.ID, r.Name, r.Executor, r.Version, e.now().UnixMilli()); err != nil {
		e.log.Error("recording runner", "err", err)
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
