package work

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Tx is a transaction.
type Tx interface {
	DB
	Commit() error
	Rollback() error
}

// Database starts transactions and says which database it is.
type Database interface {
	DB
	BeginTx(ctx context.Context) (Tx, error)
	Postgres() bool
}

// Store is the work tables.
type Store struct {
	db     Database
	now    func() time.Time
	chains Chains
	mu     sync.Mutex
	cache  map[Scope]cached
	hook   Hook
	closed map[string]bool
}

type cached struct {
	eff   *Effective
	until time.Time
}

// cacheFor is how long resolved settings are reused.
const cacheFor = 5 * time.Second

// New makes a store.
func New(db Database) *Store { return &Store{db: db, now: time.Now} }

// SetClock replaces the clock, for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Now is the store's time.
func (s *Store) Now() time.Time { return s.now() }

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// tx runs f in one transaction, retried a few times when another writer got
// there first.
func (s *Store) tx(ctx context.Context, f func(DB) error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var t Tx
		if t, err = s.db.BeginTx(ctx); err != nil {
			return err
		}
		if err = f(t); err == nil {
			if err = t.Commit(); err == nil {
				return nil
			}
		} else {
			_ = t.Rollback()
		}
		if !s.retryable(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	return err
}

// retryable: a unique violation or serialization failure.
func (s *Store) retryable(err error) bool {
	if err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrConflict) {
		return false
	}
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "deadlock") || strings.Contains(m, "serialize") || strings.Contains(m, "could not serialize") || strings.Contains(m, "database is locked")
}

func newID(prefix string) string { return ids.New(prefix) }

// forUpdate is the clause that locks a row on PostgreSQL (SQLite's transaction
// already holds the write lock).
func (s *Store) forUpdate() string {
	if s.db.Postgres() {
		return " FOR UPDATE"
	}
	return ""
}

// derivePrefix makes a prefix from a scope's name: SHOP for acme/shop-api.
func derivePrefix(sc Scope) string {
	name := sc.ID
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 5 {
			break
		}
	}
	if b.Len() == 0 {
		switch sc.Kind {
		case ScopeOrganization:
			return "WING"
		case ScopeInstallation:
			return "BASE"
		}
		return "WORK"
	}
	return b.String()
}

// ensureScope makes the scope's record, with a prefix nobody else has.
func (s *Store) ensureScope(ctx context.Context, tx DB, sc Scope) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_scopes WHERE scope_kind = ? AND scope_id = ?`, sc.Kind, sc.ID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	base := derivePrefix(sc)
	prefix := base
	for i := 2; ; i++ {
		var taken int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM work_scopes WHERE prefix = ?`, prefix).Scan(&taken); err != nil {
			return err
		}
		if taken == 0 {
			break
		}
		prefix = fmt.Sprintf("%s%d", base, i)
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_scopes(scope_kind, scope_id, prefix, next_number, settings, created_at) VALUES(?,?,?,?,?,?)`,
		sc.Kind, sc.ID, prefix, 1, `{}`, ms(s.now()))
	return err
}

// Prefix returns the prefix of a scope's refs (making the scope's record if
// it has none yet).
func (s *Store) Prefix(ctx context.Context, sc Scope) (string, error) {
	var p string
	err := s.db.QueryRow(ctx, `SELECT prefix FROM work_scopes WHERE scope_kind = ? AND scope_id = ?`, sc.Kind, sc.ID).Scan(&p)
	if isNoRows(err) {
		if err := s.tx(ctx, func(tx DB) error { return s.ensureScope(ctx, tx, sc) }); err != nil {
			return "", err
		}
		err = s.db.QueryRow(ctx, `SELECT prefix FROM work_scopes WHERE scope_kind = ? AND scope_id = ?`, sc.Kind, sc.ID).Scan(&p)
	}
	return p, err
}

// ScopeOfPrefix finds the scope a prefix belongs to.
func (s *Store) ScopeOfPrefix(ctx context.Context, prefix string) (Scope, error) {
	var sc Scope
	err := s.db.QueryRow(ctx, `SELECT scope_kind, scope_id FROM work_scopes WHERE prefix = ?`, strings.ToUpper(prefix)).Scan(&sc.Kind, &sc.ID)
	if isNoRows(err) {
		return sc, notFound("no project has the prefix " + prefix)
	}
	return sc, err
}

// allocate gives the next number of a scope.
func (s *Store) allocate(ctx context.Context, tx DB, sc Scope) (int, error) {
	if err := s.ensureScope(ctx, tx, sc); err != nil {
		return 0, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT next_number FROM work_scopes WHERE scope_kind = ? AND scope_id = ?`+s.forUpdate(), sc.Kind, sc.ID).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE work_scopes SET next_number = ? WHERE scope_kind = ? AND scope_id = ?`, n+1, sc.Kind, sc.ID); err != nil {
		return 0, err
	}
	return n, nil
}

// reserve makes the next number of a scope at least n (for an import that
// keeps the numbers it came with).
func (s *Store) reserve(ctx context.Context, tx DB, sc Scope, n int) error {
	if err := s.ensureScope(ctx, tx, sc); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE work_scopes SET next_number = ? WHERE scope_kind = ? AND scope_id = ? AND next_number <= ?`, n+1, sc.Kind, sc.ID, n)
	return err
}

// Event is one entry of an item's history.
type Event struct {
	Seq       int64          `json:"seq"`
	ItemID    string         `json:"item_id,omitempty"`
	At        string         `json:"at"`
	Actor     string         `json:"actor"`
	ActorKind string         `json:"actor_kind"`
	Via       string         `json:"via,omitempty"`
	Kind      string         `json:"kind"`
	Data      map[string]any `json:"data,omitempty"`
}

// event appends to an item's history; at is the time (0 is now).
func (s *Store) event(ctx context.Context, tx DB, itemID string, a Actor, kind string, at int64, data map[string]any) error {
	if at == 0 {
		at = ms(s.now())
	}
	raw := "{}"
	if len(data) > 0 {
		b, _ := json.Marshal(data)
		raw = string(b)
	}
	if a.Kind == "" {
		a.Kind = KindPerson
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_events(item_id, at, actor, actor_kind, via, kind, data) VALUES(?,?,?,?,?,?,?)`,
		itemID, at, a.Name, a.Kind, a.Via, kind, raw)
	return err
}

// History is an item's events, oldest first, a page at a time.
func (s *Store) History(ctx context.Context, itemID string, after int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `SELECT seq, at, actor, actor_kind, via, kind, data FROM work_events WHERE item_id = ? AND seq > ? ORDER BY seq LIMIT ?`, itemID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at int64
		var raw string
		if err := rows.Scan(&e.Seq, &at, &e.Actor, &e.ActorKind, &e.Via, &e.Kind, &raw); err != nil {
			return nil, err
		}
		e.ItemID, e.At = itemID, iso(at)
		_ = json.Unmarshal([]byte(raw), &e.Data)
		out = append(out, e)
	}
	return out, rows.Err()
}

// DB exposes the database the store uses, for tests and the importers that
// read the archive's tables.
func (s *Store) DB() DB { return s.db }
