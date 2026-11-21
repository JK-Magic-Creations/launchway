package engine

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func TestClassifyDBError(t *testing.T) {
	pg := func(code string) error { return &pgconn.PgError{Code: code} }
	for _, c := range []struct {
		name string
		err  error
		want dbClass
	}{
		{"refused", errRefused, classNotRun},
		{"refused text", errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"), classNotRun},
		{"reset", fmt.Errorf("read: %w", syscall.ECONNRESET), classMaybeRan},
		{"eof", io.ErrUnexpectedEOF, classMaybeRan},
		{"bad conn", driver.ErrBadConn, classMaybeRan},
		{"failover text", errors.New("terminating connection due to failover"), classMaybeRan},
		{"admin shutdown", pg("57P01"), classMaybeRan},
		{"starting up", pg("57P03"), classNotRun},
		{"read-only after failover", pg("25006"), classNotRun},
		{"too many connections", pg("53300"), classNotRun},
		{"serialization", pg("40001"), classNotRun},
		{"deadlock", pg("40P01"), classNotRun},
		{"connection exception", pg("08006"), classMaybeRan},
		{"unique violation", pg("23505"), classPermanent},
		{"foreign key", pg("23503"), classPermanent},
		{"syntax", pg("42601"), classPermanent},
		{"no rows", sql.ErrNoRows, classPermanent},
		{"nil", nil, classPermanent},
	} {
		if got := classifyDBError(c.err); got != c.want {
			t.Errorf("%s: class %d, want %d", c.name, got, c.want)
		}
	}
}

// faulty opens an engine's database behind the failure-injection wrapper.
func faulty(t *testing.T) (*Engine, *faultDB, *time.Duration) {
	t.Helper()
	addr := testdb.New(t)
	e := open(t, addr, &clock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)})
	t.Cleanup(func() { e.Close() })
	f, slept := faultify(t, e.db, addr)
	return e, f, slept
}

func TestStoreRetriesARefusedStatementThenSucceeds(t *testing.T) {
	e, f, slept := faulty(t)
	f.setDown(true)
	// The database comes back after the third wait.
	waits := 0
	e.db.sleepFn = func(d time.Duration) {
		*slept += d
		if waits++; waits == 3 {
			f.setDown(false)
		}
	}
	if _, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES('x', '1')`); err != nil {
		t.Fatalf("the insert should have waited for the database: %v", err)
	}
	var v string
	if err := e.db.QueryRow(`SELECT value FROM meta WHERE key = 'x'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("value %q, err %v", v, err)
	}
	if waits < 3 {
		t.Fatalf("waited %d times", waits)
	}
}

func TestStoreGivesUpAfterTheStallBudgetWithUnavailable(t *testing.T) {
	e, f, slept := faulty(t)
	e.db.stall = 40 * time.Second
	f.setDown(true)
	_, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES('y', '1')`)
	var ue *UnavailableError
	if !errors.Is(err, ErrUnavailable) || !errors.As(err, &ue) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	if *slept < 35*time.Second || *slept > 41*time.Second {
		t.Fatalf("waited %s of a 40 s budget", *slept)
	}
	if ue.Ambiguous {
		t.Fatal("a refused connection is not ambiguous")
	}
	rows, err := e.db.Query(`SELECT 1`)
	if err == nil {
		rows.Close()
		t.Fatal("a read during the outage should fail after the budget")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("read error %v", err)
	}
}

func TestStoreNeverRepeatsAStatementThatMayHaveRun(t *testing.T) {
	e, f, _ := faulty(t)
	f.before = func(op, q string) error {
		if op == "exec" && strings.Contains(q, "'z'") {
			return io.ErrUnexpectedEOF // the connection broke while it ran
		}
		return nil
	}
	_, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES('z', '1')`)
	var ue *UnavailableError
	if !errors.As(err, &ue) || !ue.Ambiguous {
		t.Fatalf("want an ambiguous unavailable error, got %v", err)
	}
	if n := f.count("exec"); n != 1 {
		t.Fatalf("the statement was sent %d times; a non-idempotent statement must not be repeated", n)
	}
}

func TestStoreDoesNotRetryPermanentErrors(t *testing.T) {
	e, f, slept := faulty(t)
	if _, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES('d', '1')`); err != nil {
		t.Fatal(err)
	}
	_, err := e.db.Exec(`INSERT INTO meta(key, value) VALUES('d', '2')`)
	if err == nil || errors.Is(err, ErrUnavailable) || !isConflict(err) {
		t.Fatalf("want the plain constraint error, got %v", err)
	}
	if _, err := e.db.Exec(`NOT SQL`); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("syntax error: %v", err)
	}
	if *slept != 0 {
		t.Fatalf("permanent errors waited %s", *slept)
	}
	var x int
	if err := e.db.QueryRow(`SELECT 1 FROM meta WHERE key = 'nope'`).Scan(&x); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("not found: %v", err)
	}
	_ = f
}

func TestWholeTransactionIsRepeatedButACutCommitIsNot(t *testing.T) {
	e, f, _ := faulty(t)
	runs := 0
	f.before = func(op, q string) error {
		if op == "exec" && strings.Contains(q, "'tx'") && runs == 1 {
			return io.ErrUnexpectedEOF // first try dies in the middle
		}
		return nil
	}
	err := e.db.retryTx(0, false, func() error {
		runs++
		tx, err := e.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('tx', '1')`); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil || runs != 2 {
		t.Fatalf("runs %d, err %v", runs, err)
	}
	// The commit's answer is lost: the row is stored, and it is not repeated.
	f.before = nil
	f.afterCommit = io.ErrUnexpectedEOF
	runs = 0
	err = e.db.retryTx(0, false, func() error {
		runs++
		tx, err := e.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES('tx2', '1')`); err != nil {
			return err
		}
		return tx.Commit()
	})
	var ue *UnavailableError
	if !errors.As(err, &ue) || !ue.Ambiguous || runs != 1 {
		t.Fatalf("a cut commit must be reported, not repeated: runs %d err %v", runs, err)
	}
}

// A command on a run rides through an outage that ends within the budget:
// the run is created once.
func TestCreateRunRidesThroughABriefOutage(t *testing.T) {
	e, f, _ := faulty(t)
	f.setDown(true)
	waits := 0
	e.db.sleepFn = func(time.Duration) {
		if waits++; waits == 4 {
			f.setDown(false)
		}
	}
	r, err := e.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, fanIn), Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatalf("the run should have been created after the outage: %v", err)
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE id = ?`, r.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("runs %d, err %v", n, err)
	}
}
