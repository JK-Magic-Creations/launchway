package engine

import (
	"context"
	"database/sql"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// The wall between Bases, as the engine hosts it (internal/store holds the
// rules; docs/development/the-wall.md explains them).

// wallState is the engine's hold on the store.
type wallState struct {
	env      *store.Env
	registry *store.Registry
	identity *store.Identity
}

// dbxConn lets the store run on the engine's database handle, so its
// statements get the same rebinding and the same wait through a failover as
// every other statement.
type dbxConn struct{ d *dbx }

func (c dbxConn) Exec(q string, a ...any) (sql.Result, error) { return c.d.Exec(q, a...) }
func (c dbxConn) Query(q string, a ...any) (*sql.Rows, error) { return c.d.Query(q, a...) }
func (c dbxConn) QueryRow(q string, a ...any) store.Row       { return c.d.QueryRow(q, a...) }
func (c dbxConn) Begin() (store.Tx, error) {
	t, err := c.d.Begin()
	if err != nil {
		return nil, err
	}
	return txConn{t}, nil
}

type txConn struct{ t *txx }

func (c txConn) Exec(q string, a ...any) (sql.Result, error) { return c.t.Exec(q, a...) }
func (c txConn) Query(q string, a ...any) (*sql.Rows, error) { return c.t.Query(q, a...) }
func (c txConn) QueryRow(q string, a ...any) store.Row       { return c.t.QueryRow(q, a...) }
func (c txConn) Commit() error                               { return c.t.Commit() }
func (c txConn) Rollback() error                             { return c.t.Rollback() }

// openWall builds the store on the engine's database. With several Bases on
// it first makes the identity keys composite (once, one-way) and, on
// PostgreSQL, puts row-level security on every Base table.
func (e *Engine) openWall(multi bool) error {
	env := &store.Env{DB: dbxConn{e.db}, PG: e.db.pg, WriteLock: e.writeLock}
	e.wall = wallState{env: env, registry: store.NewRegistry(env, multi), identity: store.NewIdentity(env)}
	if !multi {
		return nil
	}
	ctx := context.Background()
	if err := env.ContractKeys(ctx, schemaWall); err != nil {
		return err
	}
	if err := env.EnsureRLS(ctx); err != nil {
		return err
	}
	if role, err := env.CheckRole(ctx); err == nil && e.db.pg && !role.Restricted {
		e.log.Warn("several Bases are on, but this database role is not bound by row-level security; the second lock of the wall is off until the server connects as a role that owns no tables and has no BYPASSRLS",
			"role", role.Name, "detail", role.Description)
	}
	return nil
}

// Bases is how the router, the sign-in path and the background jobs obtain
// a store.Scope. The architecture test limits who may call its doors.
func (e *Engine) Bases() *store.Registry { return e.wall.registry }

// Identity is the store of people, sessions and tokens; every method takes a
// Scope from Bases().
func (e *Engine) Identity() *store.Identity { return e.wall.identity }

// BackgroundContext is the context for work that has no request: console commands and jobs.
func (e *Engine) BackgroundContext() context.Context { return context.Background() }

// StoreEnv returns the store's environment, for tests and the console
// commands that need a second connection under another database role.
func (e *Engine) StoreEnv() *store.Env { return e.wall.env }

// ident and scope0 are how the methods that predate Bases act: for the
// default Base, with no context of their own.
func (e *Engine) ident() *store.Identity { return e.wall.identity }
func (e *Engine) scope0() store.Scope    { return e.wall.registry.DefaultScope() }
