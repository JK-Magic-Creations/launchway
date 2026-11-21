package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// DefaultStallBudget is how long a request waits for a database that is
// briefly gone (a failover, a restart) before it is answered with 503.
const DefaultStallBudget = 120 * time.Second

// ErrUnavailable means the database could not be used within the stall
// budget. Nothing was lost: the caller may repeat the request. The API
// answers it with 503 and Retry-After.
var ErrUnavailable = errors.New("the database is not available")

// UnavailableError is the error behind ErrUnavailable. Waited is how long
// the request tried; Ambiguous is set when a commit was cut off, so that the
// change may or may not have been stored (callers repeat requests that are
// safe to repeat).
type UnavailableError struct {
	Err       error
	Waited    time.Duration
	Ambiguous bool
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("the database is not available (tried for %s): %v", e.Waited.Round(time.Second), e.Err)
}
func (e *UnavailableError) Unwrap() error        { return e.Err }
func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// RetryAfter is how many seconds a caller should wait before repeating.
func (e *UnavailableError) RetryAfter() int { return 5 }

// dbClass says what a database error means for trying again.
type dbClass int

const (
	// classPermanent: a constraint, a syntax error, a missing row, anything
	// that would fail the same way again.
	classPermanent dbClass = iota
	// classNotRun: the statement was refused before it ran (no connection,
	// too many connections, a read-only standby, a deadlock or
	// serialisation rollback). Repeating it is always safe.
	classNotRun
	// classMaybeRan: the connection broke while the statement ran, so it
	// may have been applied. Safe to repeat only if it is a read, or if the
	// whole transaction is repeated and the failure came before the commit.
	classMaybeRan
)

// classifyDBError is the one place that decides which database errors are
// transient (worth waiting for) and which are permanent.
func classifyDBError(err error) dbClass {
	if err == nil || errors.Is(err, sql.ErrNoRows) || errors.Is(err, context.Canceled) {
		return classPermanent
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch c := pgErr.Code; {
		case c == "40001" || c == "40P01": // serialisation failure, deadlock: rolled back
			return classNotRun
		case c == "25006", // read-only transaction: a standby after a failover
			c == "53300",               // too many connections
			c == "57P03",               // the database is starting up
			c == "08001", c == "08004": // could not establish the connection
			return classNotRun
		case c == "57P01" || c == "57P02": // administrator shutdown, crash shutdown
			return classMaybeRan
		case strings.HasPrefix(c, "08"): // other connection exceptions
			return classMaybeRan
		}
		return classPermanent
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return classNotRun // nothing was sent
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return classNotRun
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, net.ErrClosed) {
		return classMaybeRan
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return classMaybeRan
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no connection to the server"),
		strings.Contains(msg, "database system is starting up"),
		strings.Contains(msg, "too many clients"),
		strings.Contains(msg, "cannot execute") && strings.Contains(msg, "read-only transaction"):
		return classNotRun
	case strings.Contains(msg, "connection reset"), strings.Contains(msg, "broken pipe"),
		strings.Contains(msg, "unexpected eof"), strings.Contains(msg, "conn closed"),
		strings.Contains(msg, "failover"), strings.Contains(msg, "database system is shutting down"),
		strings.Contains(msg, "bad connection"):
		return classMaybeRan
	}
	return classPermanent
}

// IsTransientDBError reports whether err is a database failure that passes
// by itself (so the request may be repeated later).
func IsTransientDBError(err error) bool {
	return errors.Is(err, ErrUnavailable) || classifyDBError(err) != classPermanent
}

// commitError marks an error from COMMIT that may have left the change
// stored: such a transaction is not repeated unless the caller says it is
// safe to.
type commitError struct{ error }

func (c *commitError) Unwrap() error { return c.error }

// retrier decides, after each failed try, whether and when to try again:
// conflicts between servers a few times quickly (as before), transient
// database errors with capped, jittered backoff until the stall budget is
// spent.
type retrier struct {
	d            *dbx
	maxConflicts int
	rerunnable   bool // a statement that may have run may be run again (reads; whole transactions that were rolled back)
	conflicts    int
	started      time.Time
	pause        time.Duration
}

// retrier starts a retry run. maxConflicts is how often a conflict may be
// retried (0 for none); rerunnable says that an attempt cut off in the
// middle may be repeated.
func (d *dbx) retrier(maxConflicts int, rerunnable bool) *retrier {
	return &retrier{d: d, maxConflicts: maxConflicts, rerunnable: rerunnable, started: d.clock(), pause: 250 * time.Millisecond}
}

func (d *dbx) stallBudget() time.Duration {
	if d.stall > 0 {
		return d.stall
	}
	return DefaultStallBudget
}

// next is given the error of a failed try. It returns nil, after waiting,
// if the work should be tried again, and otherwise the error to return.
func (r *retrier) next(err error) error {
	if err == nil {
		return nil
	}
	if isConflict(err) && r.maxConflicts > 0 {
		// Another transaction got there first: try again on fresh state,
		// a few times, quickly and at random so servers spread out.
		r.conflicts++
		if r.conflicts >= r.maxConflicts {
			return err
		}
		r.d.sleep(time.Duration(rand.Int64N(int64(r.conflicts)*int64(2*time.Millisecond))) + time.Millisecond)
		return nil
	}
	var ce *commitError
	cls := classifyDBError(err)
	switch {
	case cls == classPermanent:
		return err
	case cls == classMaybeRan && (!r.rerunnable || errors.As(err, &ce)):
		return &UnavailableError{Err: err, Waited: r.d.clock().Sub(r.started), Ambiguous: true}
	}
	waited := r.d.clock().Sub(r.started)
	if waited >= r.d.stallBudget() {
		return &UnavailableError{Err: err, Waited: waited}
	}
	p := r.pause
	r.pause = min(2*r.pause, 5*time.Second)
	p = p/2 + time.Duration(rand.Int64N(int64(p/2)+1))
	p = min(p, r.d.stallBudget()-waited)
	r.d.sleep(p)
	return nil
}

// rowx is the result of QueryRow. On the database (not inside a
// transaction) a transient failure is retried when the row is read.
type rowx struct {
	row *sql.Row
	run func() *sql.Row // runs the query again; nil inside a transaction
	d   *dbx
}

func (r *rowx) Scan(dest ...any) error {
	rt := (*retrier)(nil)
	for {
		err := r.row.Scan(dest...)
		if err == nil || r.run == nil || r.d == nil {
			return err
		}
		if rt == nil {
			rt = r.d.retrier(0, true)
		}
		if e := rt.next(err); e != nil {
			return e
		}
		r.row = r.run()
	}
}

func (r *rowx) Err() error { return r.row.Err() }

// retryTx runs one whole transaction (begin to commit) and repeats it when
// a conflict or a passing database failure rolled it back. fn must be safe to
// repeat: it may have no effects outside the transaction. A failure of the
// commit itself is ambiguous and is not repeated unless idempotent is set.
func (d *dbx) retryTx(maxConflicts int, idempotent bool, fn func() error) error {
	rt := d.retrier(maxConflicts, true)
	for {
		err := fn()
		if err == nil {
			return nil
		}
		if idempotent {
			var ce *commitError
			if errors.As(err, &ce) {
				err = ce.error
			}
		}
		if e := rt.next(err); e != nil {
			return e
		}
	}
}

// retryConf is how a dbx waits for a database that is briefly gone.
type retryConf struct {
	// stall is how long a request waits; zero means DefaultStallBudget.
	stall time.Duration
	// sleepFn and nowFn replace time.Sleep and time.Now in tests.
	sleepFn func(time.Duration)
	nowFn   func() time.Time
}

func (d *dbx) clock() time.Time {
	if d.nowFn != nil {
		return d.nowFn()
	}
	return time.Now()
}

// Exec runs a statement. A passing failure is retried, within the stall
// budget, only if the statement was refused before it ran: one cut off in the
// middle may have been applied, and is reported as unavailable instead.
func (d *dbx) Exec(q string, a ...any) (sql.Result, error) {
	q = rebind(d.pg, q)
	rt := d.retrier(0, false)
	for {
		res, err := d.DB.Exec(q, a...)
		if err == nil {
			return res, nil
		}
		if e := rt.next(err); e != nil {
			return nil, e
		}
	}
}

// Query runs a read; a passing failure is retried within the stall budget.
func (d *dbx) Query(q string, a ...any) (*sql.Rows, error) {
	q = rebind(d.pg, q)
	rt := d.retrier(0, true)
	for {
		rows, err := d.DB.Query(q, a...)
		if err == nil {
			return rows, nil
		}
		if e := rt.next(err); e != nil {
			return nil, e
		}
	}
}

func (d *dbx) QueryRow(q string, a ...any) *rowx {
	q = rebind(d.pg, q)
	run := func() *sql.Row { return d.DB.QueryRow(q, a...) }
	return &rowx{row: run(), run: run, d: d}
}

// Begin starts a transaction. Nothing has run yet, so a passing failure is
// always retried within the stall budget.
func (d *dbx) Begin() (*txx, error) {
	rt := d.retrier(0, true)
	for {
		tx, err := d.DB.Begin()
		if err == nil {
			return &txx{Tx: tx, pg: d.pg}, nil
		}
		if e := rt.next(err); e != nil {
			return nil, e
		}
	}
}

func (d *dbx) sleep(t time.Duration) {
	if d.sleepFn != nil {
		d.sleepFn(t)
		return
	}
	time.Sleep(t)
}

func (t *txx) Exec(q string, a ...any) (sql.Result, error) { return t.Tx.Exec(rebind(t.pg, q), a...) }
func (t *txx) Query(q string, a ...any) (*sql.Rows, error) { return t.Tx.Query(rebind(t.pg, q), a...) }
func (t *txx) QueryRow(q string, a ...any) *rowx {
	return &rowx{row: t.Tx.QueryRow(rebind(t.pg, q), a...)}
}

// Commit commits; a failure that may have left the change stored is marked
// (see commitError) so that it is not repeated blindly.
func (t *txx) Commit() error {
	err := t.Tx.Commit()
	if err != nil && classifyDBError(err) == classMaybeRan {
		return &commitError{err}
	}
	return err
}

// SetStallBudget sets how long a request waits for a database that is
// briefly gone (default DefaultStallBudget); zero restores the default.
func (e *Engine) SetStallBudget(d time.Duration) { e.db.stall = d }
