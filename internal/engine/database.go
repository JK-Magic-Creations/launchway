package engine

import (
	"context"
	"database/sql"
)

// Database is the engine's database as other packages (the forge's store)
// use it: the same connection pool, the same migrations, and queries written
// with ? placeholders for both SQLite and PostgreSQL. The engine still owns
// the schema; a package that keeps tables here adds a migration and a marker
// to this package.
type Database struct {
	d *dbx
	e *Engine
}

// Database returns the engine's database.
func (e *Engine) Database() *Database { return &Database{d: e.db, e: e} }

// SQL is the underlying handle, for stores that take a *sql.DB (the database
// blob store).
func (db *Database) SQL() *sql.DB { return db.d.DB }

// WriteLock takes the engine's SQLite write lock (a no-op on PostgreSQL), so
// writers inside this process queue instead of failing with "database is
// locked".
func (db *Database) WriteLock() func() { return db.e.writeLock() }

// Postgres reports whether the database is PostgreSQL.
func (db *Database) Postgres() bool { return db.d.pg }

// Exec runs a statement.
func (db *Database) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return db.d.DB.ExecContext(ctx, rebind(db.d.pg, q), a...)
}

// Query runs a query.
func (db *Database) Query(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return db.d.DB.QueryContext(ctx, rebind(db.d.pg, q), a...)
}

// QueryRow runs a query that returns at most one row.
func (db *Database) QueryRow(ctx context.Context, q string, a ...any) *sql.Row {
	return db.d.DB.QueryRowContext(ctx, rebind(db.d.pg, q), a...)
}

// Tx is a transaction.
type Tx struct {
	t  *sql.Tx
	pg bool
}

// Begin starts a transaction. On SQLite it takes the write lock at once, so
// a transaction never fails half-way because another committed first.
func (db *Database) Begin(ctx context.Context) (*Tx, error) {
	tx, err := db.d.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{t: tx, pg: db.d.pg}, nil
}

// Exec runs a statement in the transaction.
func (t *Tx) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return t.t.ExecContext(ctx, rebind(t.pg, q), a...)
}

// Query runs a query in the transaction.
func (t *Tx) Query(ctx context.Context, q string, a ...any) (*sql.Rows, error) {
	return t.t.QueryContext(ctx, rebind(t.pg, q), a...)
}

// QueryRow runs a query that returns at most one row.
func (t *Tx) QueryRow(ctx context.Context, q string, a ...any) *sql.Row {
	return t.t.QueryRowContext(ctx, rebind(t.pg, q), a...)
}

// Commit commits the transaction.
func (t *Tx) Commit() error { return t.t.Commit() }

// Rollback abandons it; harmless after Commit.
func (t *Tx) Rollback() error { return t.t.Rollback() }

// IsConflict reports whether err means another transaction got there first
// (a unique violation, a serialisation failure, a deadlock), so the work
// should be retried against fresh state.
func IsConflict(err error) bool { return isConflict(err) }
