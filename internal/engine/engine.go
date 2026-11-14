// Package engine runs pipelines durably.
//
// Every change to a run is an event appended to the database. A run's state
// is rebuilt from its events, so the engine can stop at any moment (crash,
// upgrade, power cut) and carry on from exactly where it was. Steps are handed
// to runners on leases that the runner must keep renewing; a step whose
// runner disappears is queued again.
//
// Several engines (one per server) may share a PostgreSQL database. Each
// run's events are numbered (run_seq), so when two servers change the same
// run at once only one succeeds and the other retries against the new state.
// Leases live in the database, so any server can serve any runner.
package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/hub"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("conflict")
	ErrLeaseLost = errors.New("the lease is no longer valid")
	ErrClosed    = errors.New("the engine is shutting down")
	// ErrAlreadyClaimed means another server has already acted on a change
	// to a ref (see RefChange).
	ErrAlreadyClaimed = errors.New("another server has already handled this change")
)

// Options configure an Engine.
type Options struct {
	Database string        // a SQLite file, or a postgres:// URL that several servers share
	LeaseTTL time.Duration // how long a runner may go silent; default 30s
	MaxLost  int           // times a step is requeued after losing its runner; default 3
	Hub      *hub.Hub      // live updates; optional
	Logger   *slog.Logger
	Now      func() time.Time // for tests
}

// Engine is safe for concurrent use.
type Engine struct {
	db       *dbx
	hub      *hub.Hub
	log      *slog.Logger
	ttl      time.Duration
	maxLost  int
	now      func() time.Time
	instance string // tags this process's notifications, so it can ignore its own
	channel  string // the PostgreSQL notification channel

	// writeMu serialises writes on SQLite, which allows one writer at a time
	// anyway; waiting here is fairer and cheaper than SQLite's busy loop. On
	// PostgreSQL it is not used: writers to the same run are kept apart by
	// run_seq, and writers to different runs do not need to be.
	writeMu sync.Mutex

	wakeMu sync.Mutex
	wake   chan struct{}

	stop      chan struct{}
	cancelBg  context.CancelFunc
	bg        sync.WaitGroup // the reaper and the notification listener
	closeOnce sync.Once
	closeErr  error
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
	LeaseID    string               `json:"lease_id"`
	LeaseTTL   pipeline.Duration    `json:"lease_ttl"`
	RunID      string               `json:"run_id"`
	Project    string               `json:"project"`
	RunNumber  int                  `json:"run_number"`
	Pipeline   string               `json:"pipeline"`
	StepID     string               `json:"step_id"`
	Attempt    int                  `json:"attempt"`
	Image      string               `json:"image"`
	Script     string               `json:"script"`
	Env        map[string]string    `json:"env"`
	Timeout    pipeline.Duration    `json:"timeout"`
	Source     Source               `json:"source"`
	SHA        string               `json:"sha,omitempty"`
	Container  string               `json:"container"`  // which of Containers the step runs in
	Containers []pipeline.Container `json:"containers"` // every container the run needs, services included
}

// Result is how a step attempt ended, as reported by its runner.
type Result struct {
	Outcome  string            `json:"outcome"` // succeeded, failed, canceled, or lost (the machinery failed; run it again)
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
	db, err := openDB(o.Database)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		db: db, hub: o.Hub, log: o.Logger, ttl: o.LeaseTTL, maxLost: o.MaxLost, now: o.Now,
		instance: ids.New("srv"),
		wake:     make(chan struct{}),
		stop:     make(chan struct{}),
	}
	if db.pg {
		var schema string
		if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
			db.Close()
			return nil, err
		}
		e.channel = channelFor(schema)
	}
	if err := e.recoverLeases(); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancelBg = cancel
	if db.pg {
		// Listen before serving anything, so no notification is missed.
		conn, err := e.startListening(ctx)
		if err != nil {
			cancel()
			db.Close()
			return nil, err
		}
		e.bg.Add(1)
		go e.listen(ctx, conn)
	}
	e.bg.Add(1)
	go e.reaper(ctx)
	return e, nil
}

// Close stops the engine. Runs in flight stay recorded and continue when it
// (or another server sharing the database) carries on.
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		close(e.stop)
		e.cancelBg()
		e.bg.Wait()
		e.notify()
		unlock := e.writeLock()
		defer unlock()
		e.closeErr = e.db.Close()
	})
	return e.closeErr
}

// Hub returns the live update hub.
func (e *Engine) Hub() *hub.Hub { return e.hub }

// Ping checks that the database answers.
func (e *Engine) Ping(ctx context.Context) error {
	var one int
	return e.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

// writeLock takes the SQLite write lock; on PostgreSQL it does nothing.
func (e *Engine) writeLock() (unlock func()) {
	if e.db.pg {
		return func() {}
	}
	e.writeMu.Lock()
	return e.writeMu.Unlock
}

// recoverLeases prepares the leases of steps that were in flight when the
// last server stopped. If no server has been alive for a while, every lease
// gets a fresh period: a runner that is still working carries on, and one
// that is gone lets its lease expire so the step is queued again. If another
// server is running, its leases are left exactly as they are.
func (e *Engine) recoverLeases() error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := e.now()
	until := now.Add(e.ttl).UnixMilli()
	var v string
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'alive_at'`).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	alive, _ := strconv.ParseInt(v, 10, 64)
	// Leases carried over from schema 1 have no expiry yet.
	if _, err := tx.Exec(`UPDATE leases SET expires_at = ? WHERE expires_at = 0`, until); err != nil {
		return err
	}
	if now.UnixMilli()-alive > (e.ttl / 2).Milliseconds() {
		res, err := tx.Exec(`UPDATE leases SET expires_at = ? WHERE expires_at < ?`, until, until)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			e.log.Info("recovered steps that were in flight", "count", n)
		}
	}
	if err := setAlive(tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// setAlive records that a server was running at t.
func setAlive(q interface {
	Exec(string, ...any) (sql.Result, error)
}, t time.Time) error {
	_, err := q.Exec(`INSERT INTO meta(key, value) VALUES('alive_at', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, strconv.FormatInt(t.UnixMilli(), 10))
	return err
}

// ---- commands ----

// NewRun describes a run to create.
type NewRun struct {
	Project  string
	Pipeline *pipeline.Pipeline
	Trigger  Trigger
	Source   Source
	// Push, if set, is the change to a ref that this run is for. The change
	// is claimed in the same transaction that creates the run, and a
	// "started" decision is recorded; if another server claimed it first,
	// CreateRun returns ErrAlreadyClaimed and creates nothing.
	Push *RefChange
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
	trig, src := nr.Trigger, nr.Source
	err := e.runTx(runID, true, func(c *cmd) error {
		if nr.Push != nil {
			claimed, err := claimRef(c.tx, nr.Project, *nr.Push)
			if err != nil {
				return err
			}
			if !claimed {
				return ErrAlreadyClaimed
			}
		}
		// Take the next number from the project's counter. The counter row
		// is locked until this transaction ends, so servers creating runs
		// for one project at the same moment queue up briefly instead of
		// colliding, and a rolled-back creation leaves no gap.
		var number int
		if err := c.tx.QueryRow(`INSERT INTO run_counters(project, last) VALUES(?, 1)
			ON CONFLICT(project) DO UPDATE SET last = run_counters.last + 1 RETURNING last`, nr.Project).Scan(&number); err != nil {
			return err
		}
		c.emit(RunCreated, Payload{Project: nr.Project, Number: number, Pipeline: nr.Pipeline, Trigger: &trig, Source: &src})
		c.advance()
		if nr.Push != nil {
			return insertDecision(c.tx, nr.Project, PushDecision{At: c.now, Ref: nr.Push.Ref, SHA: nr.Push.New,
				Outcome: "started", Detail: fmt.Sprintf("started run #%d", number), RunID: runID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if nr.Push != nil {
		e.pruneDecisions(nr.Project)
	}
	return e.runSummary(runID)
}

// Cancel asks a run to stop. Steps not yet running are canceled at once;
// running steps are stopped by their runners, which learn of it from their
// next heartbeat.
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

// recheckEvery bounds how long a waiting Lease relies on notifications from
// other servers alone; a missed notification costs at most this long.
const recheckEvery = 2 * time.Second

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
		if e.db.pg {
			left = min(left, recheckEvery)
		}
		t := time.NewTimer(left)
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-e.stop:
			t.Stop()
			return nil, ErrClosed
		}
	}
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, cd := range cands {
		var job *Job
		err := e.withRun(cd.run, func(c *cmd) error {
			job = nil // the closure may run more than once
			s := c.st.Steps[cd.step]
			if s == nil || s.Status != StepQueuedS || s.Attempt != cd.attempt || c.st.CancelRequested {
				return nil // someone else got it, or it changed
			}
			leaseID := ids.New("lease")
			if _, err := c.tx.Exec(`INSERT INTO leases(id, run_id, step_id, attempt, runner, expires_at) VALUES(?,?,?,?,?,?)`,
				leaseID, cd.run, cd.step, s.Attempt, r.ID, c.leaseUntil()); err != nil {
				return err
			}
			c.emit(StepLeased, Payload{Step: cd.step, Attempt: s.Attempt, Lease: leaseID, Runner: r.ID})
			job = c.job(cd.step, leaseID)
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

// leaseRef is where a lease points.
type leaseRef struct {
	runID, stepID string
	attempt       int
}

// findLease looks a lease up. Any server can do this, whichever one granted
// the lease.
func (e *Engine) findLease(leaseID string) (leaseRef, error) {
	var l leaseRef
	err := e.db.QueryRow(`SELECT run_id, step_id, attempt FROM leases WHERE id = ?`, leaseID).Scan(&l.runID, &l.stepID, &l.attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrLeaseLost
	}
	return l, err
}

// Start records that the runner began executing the step. It reports whether
// the run has been asked to cancel.
func (e *Engine) Start(leaseID string) (cancel bool, err error) {
	l, err := e.findLease(leaseID)
	if err != nil {
		return false, err
	}
	err = e.withRun(l.runID, func(c *cmd) error {
		cancel = false
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || !s.Status.Active() {
			return ErrLeaseLost
		}
		if err := c.renewLease(leaseID); err != nil {
			return err
		}
		if s.Status == StepLeasedS {
			c.emit(StepStarted, Payload{Step: l.stepID, Attempt: l.attempt, Lease: leaseID})
		}
		cancel = c.st.CancelRequested
		return nil
	})
	return cancel, err
}

// Heartbeat renews a lease. It reports whether the run has been asked to
// cancel, in which case the runner should stop the step. A lease stays valid
// until it is reaped, so a heartbeat that arrives late but before the reaper
// still counts.
func (e *Engine) Heartbeat(leaseID string) (cancel bool, err error) {
	unlock := e.writeLock()
	defer unlock()
	var runID string
	err = e.db.QueryRow(`UPDATE leases SET expires_at = ? WHERE id = ? RETURNING run_id`,
		e.now().Add(e.ttl).UnixMilli(), leaseID).Scan(&runID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrLeaseLost
	}
	if err != nil {
		return false, err
	}
	var flag int
	if err := e.db.QueryRow(`SELECT cancel_requested FROM runs WHERE id = ?`, runID).Scan(&flag); err != nil {
		return false, err
	}
	return flag != 0, nil
}

// Finish records how a step attempt ended.
func (e *Engine) Finish(leaseID string, res Result) error {
	l, err := e.findLease(leaseID)
	if err != nil {
		return err
	}
	return e.withRun(l.runID, func(c *cmd) error {
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || !s.Status.Active() {
			return ErrLeaseLost
		}
		if err := c.endLease(leaseID); err != nil {
			return err
		}
		p := Payload{Step: l.stepID, Attempt: l.attempt, Lease: leaseID, ExitCode: res.ExitCode, Outputs: res.Outputs}
		switch res.Outcome {
		case "succeeded":
			p.Outcome = string(StepSucceeded)
			c.emit(StepFinished, p)
		case "lost":
			if c.st.CancelRequested {
				p.Reason = "canceled"
				c.emit(StepCanceled, p)
			} else {
				c.lose(l.stepID, nonEmpty(res.Error, "the runner could not run the step"))
			}
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
}

// Release hands back a step that was leased but never started, for example
// because the runner disconnected before receiving it. It is queued again
// at once, as the same attempt.
func (e *Engine) Release(leaseID string) error {
	l, err := e.findLease(leaseID)
	if err != nil {
		return err
	}
	return e.withRun(l.runID, func(c *cmd) error {
		s := c.st.Steps[l.stepID]
		if s == nil || s.LeaseID != leaseID || s.Status != StepLeasedS {
			return ErrLeaseLost
		}
		if err := c.endLease(leaseID); err != nil {
			return err
		}
		if c.st.CancelRequested {
			c.emit(StepCanceled, Payload{Step: l.stepID, Attempt: s.Attempt, Reason: "the run was canceled"})
		} else {
			c.emit(StepQueued, Payload{Step: l.stepID, Attempt: s.Attempt, Reason: "handed back before it started"})
		}
		c.advance()
		return nil
	})
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
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Taking the sequence numbers locks the lease row, so concurrent calls
	// for the same lease (on any server) get separate ranges.
	var l leaseRef
	var last int
	err = tx.QueryRow(`UPDATE leases SET log_seq = log_seq + ?, expires_at = ? WHERE id = ?
		RETURNING run_id, step_id, attempt, log_seq`, len(lines), e.now().Add(e.ttl).UnixMilli(), leaseID).
		Scan(&l.runID, &l.stepID, &l.attempt, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	first := last - len(lines) + 1
	out := make([]LogLine, len(lines))
	for i, ln := range lines {
		if len(ln.Text) > MaxLogLineBytes {
			ln.Text = ln.Text[:MaxLogLineBytes] + " …[line truncated]"
		}
		if ln.At == 0 {
			ln.At = e.now().UnixMilli()
		}
		if ln.Stream == "" {
			ln.Stream = "stdout"
		}
		ln.Seq = first + i
		out[i] = ln
		if _, err := tx.Exec(`INSERT INTO logs(run_id, step_id, attempt, seq, at, stream, text) VALUES(?,?,?,?,?,?,?)`,
			l.runID, l.stepID, l.attempt, ln.Seq, ln.At, ln.Stream, ln.Text); err != nil {
			return err
		}
	}
	if err := e.notifyOthers(tx, note{Kind: noteLog, Run: l.runID, Step: l.stepID, Attempt: l.attempt, FromSeq: first, ToSeq: last}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
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

// reaper runs on every server. Each tick it records that a server is alive
// and expires leases whose runners have gone quiet.
func (e *Engine) reaper(ctx context.Context) {
	defer e.bg.Done()
	interval := min(max(e.ttl/10, 50*time.Millisecond), time.Second)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.markAlive()
			e.reap()
		}
	}
}

func (e *Engine) markAlive() {
	unlock := e.writeLock()
	defer unlock()
	if err := setAlive(e.db, e.now()); err != nil {
		e.log.Error("recording that this server is alive", "err", err)
	}
}

// reap expires leases that have run out. Several servers may reap at once;
// each lease is ended by exactly one of them, because the row is deleted in
// the same transaction that records the step as lost.
func (e *Engine) reap() {
	rows, err := e.db.Query(`SELECT id, run_id, step_id, runner FROM leases WHERE expires_at < ?`, e.now().UnixMilli())
	if err != nil {
		e.log.Error("looking for expired leases", "err", err)
		return
	}
	type expired struct{ id, runID, stepID, runner string }
	var found []expired
	for rows.Next() {
		var l expired
		if err := rows.Scan(&l.id, &l.runID, &l.stepID, &l.runner); err != nil {
			rows.Close()
			e.log.Error("looking for expired leases", "err", err)
			return
		}
		found = append(found, l)
	}
	rows.Close()
	for _, l := range found {
		err := e.withRun(l.runID, func(c *cmd) error {
			// Since the query above, another server may have reaped the
			// lease or the runner may have renewed it.
			res, err := c.tx.Exec(`DELETE FROM leases WHERE id = ? AND expires_at < ?`, l.id, c.now)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil || n == 0 {
				return err
			}
			s := c.st.Steps[l.stepID]
			if s != nil && s.LeaseID == l.id && s.Status.Active() {
				c.lose(l.stepID, "the runner stopped responding")
				c.advance()
				c.after(func() { e.log.Warn("runner stopped responding", "run", l.runID, "step", l.stepID, "runner", l.runner) })
			}
			return nil
		})
		if errors.Is(err, ErrNotFound) {
			// A lease for a run that does not exist: nothing to recover.
			unlock := e.writeLock()
			_, err = e.db.Exec(`DELETE FROM leases WHERE id = ?`, l.id)
			unlock()
		}
		if err != nil {
			e.log.Error("expiring a lease", "lease", l.id, "err", err)
		}
	}
}

// ---- the transaction helper ----

type cmd struct {
	e       *Engine
	tx      *txx
	runID   string
	st      *RunState
	runSeq  int64 // run_seq of the last event already stored
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

// after runs fn once the transaction has committed. It never runs for an
// attempt that was rolled back.
func (c *cmd) after(fn func()) { c.hooks = append(c.hooks, fn) }

// leaseUntil is when a lease granted or renewed now runs out.
func (c *cmd) leaseUntil() int64 { return c.now + c.e.ttl.Milliseconds() }

// renewLease extends a lease inside the transaction.
func (c *cmd) renewLease(leaseID string) error {
	res, err := c.tx.Exec(`UPDATE leases SET expires_at = ? WHERE id = ?`, c.leaseUntil(), leaseID)
	return leaseChanged(res, err)
}

// endLease deletes a lease inside the transaction. Exactly one transaction
// can do this for a given lease; the others get ErrLeaseLost.
func (c *cmd) endLease(leaseID string) error {
	res, err := c.tx.Exec(`DELETE FROM leases WHERE id = ?`, leaseID)
	return leaseChanged(res, err)
}

func leaseChanged(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// maxAttempts bounds how often a command is retried because another
// transaction changed the same run first.
const maxAttempts = 10

// withRun runs fn against the run's current state and stores what it emits.
//
// fn may run several times: if another transaction (on this server or
// another) changes the run first, everything is rolled back and fn runs
// again on the new state. So fn must not have effects outside the
// transaction, other than through c.after.
func (e *Engine) withRun(runID string, fn func(c *cmd) error) error {
	return e.runTx(runID, false, fn)
}

// runTx is withRun that, if create is set, also accepts a run that does not
// exist yet.
func (e *Engine) runTx(runID string, create bool, fn func(c *cmd) error) error {
	for attempt := 1; ; attempt++ {
		c, err := e.attemptTx(runID, create, fn)
		if err == nil {
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
		if !isConflict(err) {
			return err
		}
		if attempt == maxAttempts {
			return fmt.Errorf("run %s kept changing underneath this command (%d attempts): %w", runID, attempt, err)
		}
		// Back off a little, at random, so competing servers spread out.
		time.Sleep(time.Duration(rand.Int64N(int64(attempt)*int64(2*time.Millisecond))) + time.Millisecond)
	}
}

// attemptTx is one try of runTx: one transaction, committed only if fn and
// storing its events succeed.
func (e *Engine) attemptTx(runID string, create bool, fn func(c *cmd) error) (*cmd, error) {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	evs, runSeq, err := loadEvents(tx, runID)
	if err != nil {
		return nil, err
	}
	c := &cmd{e: e, tx: tx, runID: runID, st: Fold(evs), runSeq: runSeq, now: e.now().UnixMilli(), changed: map[string]bool{}}
	if c.st == nil {
		if !create {
			return nil, fmt.Errorf("%w: run %s", ErrNotFound, runID)
		}
		c.st = &RunState{}
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	if len(c.events) > 0 {
		if err := c.persist(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}

// loadEvents returns a run's events in order, and the run_seq of the last.
func loadEvents(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, runID string) ([]Event, int64, error) {
	rows, err := q.Query(`SELECT seq, run_seq, type, at, data FROM events WHERE run_id = ? ORDER BY run_seq`, runID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var evs []Event
	var last int64
	for rows.Next() {
		ev := Event{RunID: runID}
		var data string
		if err := rows.Scan(&ev.Seq, &last, &ev.Type, &ev.At, &data); err != nil {
			return nil, 0, err
		}
		if err := json.Unmarshal([]byte(data), &ev.Data); err != nil {
			return nil, 0, fmt.Errorf("event %d: %w", ev.Seq, err)
		}
		evs = append(evs, ev)
	}
	return evs, last, rows.Err()
}

// persist stores the new events and brings the derived tables up to date.
// The events go first: if another transaction has stored the same run_seq,
// this fails with a conflict before anything else is touched.
func (c *cmd) persist() error {
	for i := range c.events {
		ev := &c.events[i]
		data, err := json.Marshal(ev.Data)
		if err != nil {
			return err
		}
		c.runSeq++
		if err := c.tx.QueryRow(`INSERT INTO events(run_id, run_seq, type, at, data) VALUES(?,?,?,?,?) RETURNING seq`,
			ev.RunID, c.runSeq, ev.Type, ev.At, string(data)).Scan(&ev.Seq); err != nil {
			return err
		}
		c.st.LastSeq = ev.Seq
	}
	st := c.st
	t := st.Trigger
	if _, err := c.tx.Exec(`INSERT INTO runs(id, project, number, pipeline, status, cancel_requested, event, ref, branch, tag, sha,
			actor, message, author, created_at, started_at, finished_at, last_seq)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status = excluded.status, cancel_requested = excluded.cancel_requested,
			started_at = excluded.started_at, finished_at = excluded.finished_at, last_seq = excluded.last_seq`,
		st.ID, st.Project, st.Number, st.Pipeline.Name, st.Status, boolInt(st.CancelRequested), t.Event, t.Ref, t.Branch, t.Tag, t.SHA,
		t.Actor, t.Message, t.Author, st.CreatedAt, st.StartedAt, st.FinishedAt, st.LastSeq); err != nil {
		return err
	}
	changed := c.changedSteps()
	for _, id := range changed {
		s := st.Steps[id]
		outputs, _ := json.Marshal(s.Outputs)
		if s.Outputs == nil {
			outputs = []byte("{}")
		}
		if _, err := c.tx.Exec(`INSERT INTO steps(run_id, step_id, position, status, attempt, queued_at, started_at, finished_at,
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
	return c.e.notifyOthers(c.tx, note{Kind: noteRun, Run: c.runID, Steps: changed, Queued: c.queued})
}

// changedSteps returns the IDs of the steps this command changed, sorted so
// that rows are always written in the same order.
func (c *cmd) changedSteps() []string {
	ids := make([]string, 0, len(c.changed))
	for id := range c.changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
	container := st.Pipeline.ContainerFor(def)
	if c := st.Pipeline.Container(container); c != nil {
		for k, v := range c.Env {
			env[k] = v
		}
	}
	for k, v := range def.Env {
		env[k] = v
	}
	for _, n := range def.Needs {
		for k, v := range st.Steps[n].Outputs {
			env["LAUNCHWAY_NEEDS_"+envName(n)+"_"+envName(k)] = v
		}
	}
	for k, v := range map[string]string{
		"CI": "true", "LAUNCHWAY": "true",
		"LAUNCHWAY_RUN_ID": st.ID, "LAUNCHWAY_RUN_NUMBER": strconv.Itoa(st.Number),
		"LAUNCHWAY_PROJECT": st.Project, "LAUNCHWAY_PIPELINE": st.Pipeline.Name,
		"LAUNCHWAY_STEP": stepID, "LAUNCHWAY_ATTEMPT": strconv.Itoa(s.Attempt),
		"LAUNCHWAY_EVENT": t.Event, "LAUNCHWAY_REF": t.Ref, "LAUNCHWAY_BRANCH": t.Branch,
		"LAUNCHWAY_TAG": t.Tag, "LAUNCHWAY_COMMIT": t.SHA,
	} {
		env[k] = v
	}
	return &Job{
		Container: container, Containers: st.Pipeline.RunContainers(),
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

// publishChanges sends the current state of a run and of the given steps to
// this server's hub, reading them back from the database.
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

// notify wakes every Lease waiting on this server.
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
	unlock := e.writeLock()
	defer unlock()
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
