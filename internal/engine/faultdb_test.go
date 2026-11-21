package engine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net"
	"net/url"
	"sync"
	"syscall"
	"testing"
	"time"
)

// faultDB is the failure-injection harness for the database layer: it
// puts a wrapper between the store and a real SQLite database that can refuse
// connections, drop live ones, and fail a statement or a commit once. A
// "commit that reached the database but whose answer was lost" is
// reproduced with dropAfterCommit.
type faultDB struct {
	mu   sync.Mutex
	down bool // refuse new connections, break live ones
	// before runs ahead of every statement ("exec", "query", "begin",
	// "commit") and may return an error to inject instead of running it.
	before func(op, query string) error
	// afterCommit, if set, makes the next commit succeed in the database and
	// then report this error, as when the answer is lost.
	afterCommit error
	ops         map[string]int
}

var errRefused = &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}

func (f *faultDB) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }
func (f *faultDB) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ops[op]
}

func (f *faultDB) check(op, q string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ops == nil {
		f.ops = map[string]int{}
	}
	f.ops[op]++
	if f.down {
		return driver.ErrBadConn
	}
	if f.before != nil {
		return f.before(op, q)
	}
	return nil
}

// faultify replaces the database behind d with one that goes through f,
// and gives it a fake clock so that backoff costs no time.
func faultify(t *testing.T, d *dbx, path string) (*faultDB, *time.Duration) {
	t.Helper()
	if d.pg {
		t.Skip("the wrapper drives SQLite; PostgreSQL has its own proxy test")
	}
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()
	probe, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	inner := probe.Driver()
	probe.Close()
	f := &faultDB{}
	old := d.DB
	d.DB = sql.OpenDB(&faultConnector{f: f, inner: inner, dsn: dsn})
	d.DB.SetMaxOpenConns(8)
	t.Cleanup(func() { d.DB.Close(); d.DB = old })
	var slept time.Duration
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	d.sleepFn = func(x time.Duration) { mu.Lock(); slept += x; now = now.Add(x); mu.Unlock() }
	d.nowFn = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	return f, &slept
}

type faultConnector struct {
	f     *faultDB
	inner driver.Driver
	dsn   string
}

func (c *faultConnector) Connect(context.Context) (driver.Conn, error) {
	c.f.mu.Lock()
	down := c.f.down
	c.f.mu.Unlock()
	if down {
		return nil, errRefused
	}
	cn, err := c.inner.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &faultConn{f: c.f, Conn: cn}, nil
}
func (c *faultConnector) Driver() driver.Driver { return c.inner }

type faultConn struct {
	f *faultDB
	driver.Conn
}

func (c *faultConn) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	if err := c.f.check("exec", q); err != nil {
		return nil, err
	}
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, q, a)
}

func (c *faultConn) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	if err := c.f.check("query", q); err != nil {
		return nil, err
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, a)
}

func (c *faultConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if err := c.f.check("begin", ""); err != nil {
		return nil, err
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return &faultTx{f: c.f, Tx: tx}, nil
}

type faultTx struct {
	f *faultDB
	driver.Tx
}

func (x *faultTx) Commit() error {
	if err := x.f.check("commit", ""); err != nil {
		x.Tx.Rollback()
		return err
	}
	if err := x.Tx.Commit(); err != nil {
		return err
	}
	x.f.mu.Lock()
	err := x.f.afterCommit
	x.f.afterCommit = nil
	x.f.mu.Unlock()
	return err
}
