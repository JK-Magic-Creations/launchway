package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"
)

// Inspected is a database opened for reading by a tool (`launchway doctor`)
// without starting an engine and without migrating: the schema is whatever
// the last server left. Queries are written with ? placeholders.
type Inspected struct{ d *dbx }

// OpenInspected opens the database at addr, as InspectDatabase does, and
// keeps it open until Close.
func OpenInspected(addr string, wait time.Duration) (*Inspected, error) {
	var d *dbx
	if isPostgres(addr) {
		db, err := sql.Open("pgx", addr)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(2)
		d = &dbx{DB: db, pg: true, dsn: addr}
	} else {
		q := url.Values{}
		q.Add("_pragma", "busy_timeout(10000)")
		db, err := sql.Open("sqlite", "file:"+addr+"?"+q.Encode())
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		d = &dbx{DB: db}
	}
	if err := connect(d, wait, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("connecting to the database: %w", err)
	}
	return &Inspected{d}, nil
}

// Close closes the database.
func (i *Inspected) Close() error { return i.d.Close() }

// Exec runs a statement.
func (i *Inspected) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return i.d.ExecContext(ctx, rebind(i.d.pg, q), a...)
}

// Query runs a query.
func (i *Inspected) Query(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return i.d.QueryContext(ctx, rebind(i.d.pg, q), a...)
}

// QueryRow runs a query that returns at most one row.
func (i *Inspected) QueryRow(ctx context.Context, q string, a ...any) *sql.Row {
	return i.d.QueryRowContext(ctx, rebind(i.d.pg, q), a...)
}

// WriteLock is a no-op: nothing else in this process writes.
func (i *Inspected) WriteLock() func() { return func() {} }

// Postgres reports whether the database is PostgreSQL.
func (i *Inspected) Postgres() bool { return i.d.pg }
