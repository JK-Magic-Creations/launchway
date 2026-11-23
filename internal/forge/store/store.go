// Package store keeps forge connections, repositories, events and webhook
// deliveries in the engine's database (tables forge_connections,
// forge_repositories, forge_events and forge_deliveries, schema 26), one
// code path for SQLite and PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database: the engine's Database satisfies
// it.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Errors of the store; test with errors.Is.
var (
	ErrNotFound = forge.ErrNotFound
	ErrExists   = forge.ErrExists
)

// Store is the forge's tables.
type Store struct {
	db  DB
	now func() time.Time
	// ReplayWindow is how long a delivery is remembered (DefaultReplayWindow when 0), and MaxDeliveries how many a
	// connection keeps (DefaultMaxDeliveries when 0): the oldest go first.
	ReplayWindow  time.Duration
	MaxDeliveries int
}

// Replay memory: kept in the database, so every server replica shares it, and bounded in time and in size.
const (
	DefaultReplayWindow  = 7 * 24 * time.Hour
	DefaultMaxDeliveries = 50000
)

// New makes a store over a database.
func New(db DB) *Store { return &Store{db: db, now: time.Now} }

// SetClock replaces the clock; for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

const connCols = `id, name, kind, base_url, cred_env, cred_file, events, enabled, created_at, updated_at, auth_kind, app_ref, owners, webhook_env, webhook_file, upload_url`

func scanConnection(sc interface{ Scan(...any) error }) (forge.Connection, error) {
	var c forge.Connection
	var kind, events string
	var enabled int
	var created, updated int64
	var owners string
	if err := sc.Scan(&c.ID, &c.Name, &kind, &c.BaseURL, &c.Credential.Env, &c.Credential.File, &events, &enabled, &created, &updated,
		&c.AuthKind, &c.AppRef, &owners, &c.WebhookSecret.Env, &c.WebhookSecret.File, &c.UploadURL); err != nil {
		return c, err
	}
	if owners != "" {
		c.Owners = strings.Split(owners, ",")
	}
	c.Kind, c.Events, c.Enabled = forge.Kind(kind), forge.EventMode(events), enabled != 0
	c.CreatedAt, c.UpdatedAt = fromMS(created), fromMS(updated)
	return c, nil
}

func authKind(k string) string {
	if k == "" {
		return "token"
	}
	return k
}

// CreateConnection validates and stores a connection, giving it an ID.
func (s *Store) CreateConnection(ctx context.Context, c forge.Connection, by string) (forge.Connection, error) {
	if err := c.Validate(); err != nil {
		return c, err
	}
	c.ID = ids.New("con")
	now := s.now()
	c.CreatedAt, c.UpdatedAt = now.UTC(), now.UTC()
	_, err := s.db.Exec(ctx, `INSERT INTO forge_connections(id, name, kind, base_url, cred_env, cred_file, events, enabled, created_at, updated_at, created_by, auth_kind, app_ref, owners, webhook_env, webhook_file, upload_url)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, string(c.Kind), c.BaseURL, c.Credential.Env, c.Credential.File, string(c.Events), b2i(c.Enabled), ms(now), ms(now), by,
		authKind(c.AuthKind), c.AppRef, strings.Join(c.Owners, ","), c.WebhookSecret.Env, c.WebhookSecret.File, c.UploadURL)
	if err != nil {
		if isUnique(err) {
			return forge.Connection{}, fmt.Errorf("%w: a connection named %q", ErrExists, c.Name)
		}
		return forge.Connection{}, err
	}
	return c, nil
}

// Connection returns a connection by ID.
func (s *Store) Connection(ctx context.Context, id string) (forge.Connection, error) {
	c, err := scanConnection(s.db.QueryRow(ctx, `SELECT `+connCols+` FROM forge_connections WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("%w: connection %s", ErrNotFound, id)
	}
	return c, err
}

// ConnectionByName returns a connection by its name.
func (s *Store) ConnectionByName(ctx context.Context, name string) (forge.Connection, error) {
	c, err := scanConnection(s.db.QueryRow(ctx, `SELECT `+connCols+` FROM forge_connections WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("%w: connection %q", ErrNotFound, name)
	}
	return c, err
}

// Connections lists every connection, oldest first.
func (s *Store) Connections(ctx context.Context) ([]forge.Connection, error) {
	rows, err := s.db.Query(ctx, `SELECT `+connCols+` FROM forge_connections ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []forge.Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetEnabled turns a connection on or off. Turning one off keeps its data.
func (s *Store) SetEnabled(ctx context.Context, id string, on bool) error {
	res, err := s.db.Exec(ctx, `UPDATE forge_connections SET enabled = ?, updated_at = ? WHERE id = ?`, b2i(on), ms(s.now()), id)
	return affected(res, err, "connection "+id)
}

// DeleteConnection removes a connection that has no repositories.
func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM forge_repositories WHERE connection_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: the connection still has %d repositories; remove them first", forge.ErrInvalid, n)
	}
	res, err := s.db.Exec(ctx, `DELETE FROM forge_connections WHERE id = ?`, id)
	return affected(res, err, "connection "+id)
}

const repoCols = `id, connection_id, home, name, path, project, default_branch, visibility, archived, mirror, clone_https, clone_ssh, created_at, updated_at`

func scanRepository(sc interface{ Scan(...any) error }) (forge.Repository, string, error) {
	var r forge.Repository
	var project, vis string
	var archived, mirror int
	var created, updated int64
	err := sc.Scan(&r.ID, &r.Connection, &r.Home, &r.Name, &r.Path, &project, &r.DefaultBranch, &vis, &archived, &mirror,
		&r.Clone.HTTPS, &r.Clone.SSH, &created, &updated)
	r.Visibility, r.Archived, r.Mirror = forge.Visibility(vis), archived != 0, mirror != 0
	r.CreatedAt, r.UpdatedAt = fromMS(created), fromMS(updated)
	return r, project, err
}

// PutRepository stores a repository of a connection, creating it or, when the
// connection already has one at that path, updating it. project names the
// watched project it stands for ("" if none). It returns the stored row.
func (s *Store) PutRepository(ctx context.Context, r forge.Repository, project string) (forge.Repository, error) {
	if r.Visibility == "" {
		r.Visibility = forge.Private
	}
	if err := r.Validate(); err != nil {
		return r, err
	}
	if r.Name == "" {
		r.Name = r.Path[strings.LastIndex(r.Path, "/")+1:]
	}
	now := s.now()
	r.UpdatedAt = now.UTC()
	res, err := s.db.Exec(ctx, `UPDATE forge_repositories SET home = ?, name = ?, project = ?, default_branch = ?, visibility = ?, archived = ?, mirror = ?,
		clone_https = ?, clone_ssh = ?, updated_at = ? WHERE connection_id = ? AND path = ?`,
		r.Home, r.Name, project, r.DefaultBranch, string(r.Visibility), b2i(r.Archived), b2i(r.Mirror), r.Clone.HTTPS, r.Clone.SSH, ms(now), r.Connection, r.Path)
	if err != nil {
		return r, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		got, _, err := scanRepository(s.db.QueryRow(ctx, `SELECT `+repoCols+` FROM forge_repositories WHERE connection_id = ? AND path = ?`, r.Connection, r.Path))
		return got, err
	}
	r.ID = ids.New("rep")
	r.CreatedAt = now.UTC()
	_, err = s.db.Exec(ctx, `INSERT INTO forge_repositories(`+repoCols+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Connection, r.Home, r.Name, r.Path, project, r.DefaultBranch, string(r.Visibility), b2i(r.Archived), b2i(r.Mirror),
		r.Clone.HTTPS, r.Clone.SSH, ms(now), ms(now))
	if err != nil && isUnique(err) {
		// Another server created it between our update and insert: it is there now.
		got, _, gerr := scanRepository(s.db.QueryRow(ctx, `SELECT `+repoCols+` FROM forge_repositories WHERE connection_id = ? AND path = ?`, r.Connection, r.Path))
		return got, gerr
	}
	return r, err
}

// Repository returns a repository by ID.
func (s *Store) Repository(ctx context.Context, id string) (forge.Repository, error) {
	r, _, err := scanRepository(s.db.QueryRow(ctx, `SELECT `+repoCols+` FROM forge_repositories WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: repository %s", ErrNotFound, id)
	}
	return r, err
}

// RepositoryByPath finds a repository by connection and path.
func (s *Store) RepositoryByPath(ctx context.Context, connection, path string) (forge.Repository, error) {
	r, _, err := scanRepository(s.db.QueryRow(ctx, `SELECT `+repoCols+` FROM forge_repositories WHERE connection_id = ? AND path = ?`, connection, path))
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: repository %s", ErrNotFound, path)
	}
	return r, err
}

// RepositoryOfProject finds the repository that stands for a watched project.
func (s *Store) RepositoryOfProject(ctx context.Context, project string) (forge.Repository, error) {
	r, _, err := scanRepository(s.db.QueryRow(ctx, `SELECT `+repoCols+` FROM forge_repositories WHERE project = ? ORDER BY created_at LIMIT 1`, project))
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: no repository for project %s", ErrNotFound, project)
	}
	return r, err
}

// Repositories lists a connection's repositories ("" for all), by path.
func (s *Store) Repositories(ctx context.Context, connection string) ([]forge.Repository, error) {
	q, args := `SELECT `+repoCols+` FROM forge_repositories`, []any(nil)
	if connection != "" {
		q, args = q+` WHERE connection_id = ?`, []any{connection}
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY connection_id, path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []forge.Repository
	for rows.Next() {
		r, _, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Event is one thing a connection heard or did, kept so that "why did this
// push not start a run?" has an answer.
type Event struct {
	Seq        int64
	Connection string
	Repository string
	Kind       forge.EventKind
	Ref        string
	Old, New   string
	Outcome    string // started, ignored, error, ...
	Detail     string
	At         time.Time
}

// AppendEvent records an event.
func (s *Store) AppendEvent(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	_, err := s.db.Exec(ctx, `INSERT INTO forge_events(connection_id, repository_id, kind, ref, old_sha, new_sha, outcome, detail, at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Connection, e.Repository, string(e.Kind), e.Ref, e.Old, e.New, e.Outcome, clip(e.Detail, 4000), ms(e.At))
	return err
}

// Events lists a connection's events, newest first. before is a sequence
// number to page from (0: the newest); repository filters when not empty.
func (s *Store) Events(ctx context.Context, connection, repository string, before int64, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT seq, connection_id, repository_id, kind, ref, old_sha, new_sha, outcome, detail, at FROM forge_events WHERE connection_id = ?`
	args := []any{connection}
	if repository != "" {
		q += ` AND repository_id = ?`
		args = append(args, repository)
	}
	if before > 0 {
		q += ` AND seq < ?`
		args = append(args, before)
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY seq DESC LIMIT `+fmt.Sprint(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var kind string
		var at int64
		if err := rows.Scan(&e.Seq, &e.Connection, &e.Repository, &kind, &e.Ref, &e.Old, &e.New, &e.Outcome, &e.Detail, &at); err != nil {
			return nil, err
		}
		e.Kind, e.At = forge.EventKind(kind), fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneEvents keeps a connection's newest keep events and drops older ones
// than maxAge (a zero maxAge keeps by count only). It returns how many were
// removed.
func (s *Store) PruneEvents(ctx context.Context, connection string, keep int, maxAge time.Duration) (int64, error) {
	var removed int64
	if keep > 0 {
		var cut sql.NullInt64
		err := s.db.QueryRow(ctx, `SELECT seq FROM forge_events WHERE connection_id = ? ORDER BY seq DESC LIMIT 1 OFFSET ?`, connection, keep-1).Scan(&cut)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if cut.Valid {
			res, err := s.db.Exec(ctx, `DELETE FROM forge_events WHERE connection_id = ? AND seq < ?`, connection, cut.Int64)
			if err != nil {
				return removed, err
			}
			n, _ := res.RowsAffected()
			removed += n
		}
	}
	if maxAge > 0 {
		res, err := s.db.Exec(ctx, `DELETE FROM forge_events WHERE connection_id = ? AND at < ?`, connection, ms(s.now().Add(-maxAge)))
		if err != nil {
			return removed, err
		}
		n, _ := res.RowsAffected()
		removed += n
	}
	return removed, nil
}

// SeeDelivery remembers a webhook delivery of a connection by its key and
// reports whether it is new, so a replay is ignored. A key remembered for
// longer than the replay window is new again. Two servers seeing the same
// delivery at once: exactly one gets true. The memory is bounded: a
// connection keeps at most MaxDeliveries keys, the oldest evicted first.
func (s *Store) SeeDelivery(ctx context.Context, connection, delivery string) (bool, error) {
	if connection == "" || delivery == "" {
		return false, fmt.Errorf("%w: a delivery needs a connection and an ID", forge.ErrInvalid)
	}
	win := s.ReplayWindow
	if win <= 0 {
		win = DefaultReplayWindow
	}
	key := clip(delivery, 200)
	if _, err := s.db.Exec(ctx, `DELETE FROM forge_deliveries WHERE connection_id = ? AND delivery_id = ? AND at < ?`,
		connection, key, ms(s.now().Add(-win))); err != nil {
		return false, err
	}
	res, err := s.db.Exec(ctx, `INSERT INTO forge_deliveries(connection_id, delivery_id, at) VALUES(?, ?, ?) ON CONFLICT DO NOTHING`,
		connection, key, ms(s.now()))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := s.evictDeliveries(ctx, connection); err != nil {
			return true, err
		}
	}
	return n > 0, nil
}

// evictDeliveries drops the oldest keys of a connection beyond the cap. It looks only when a new key is
// written, and removes at once what is beyond the cap plus a tenth, so it runs rarely.
func (s *Store) evictDeliveries(ctx context.Context, connection string) error {
	limit := s.MaxDeliveries
	if limit <= 0 {
		limit = DefaultMaxDeliveries
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM forge_deliveries WHERE connection_id = ?`, connection).Scan(&n); err != nil {
		return err
	}
	if n <= limit {
		return nil
	}
	keep := limit - limit/10
	_, err := s.db.Exec(ctx, `DELETE FROM forge_deliveries WHERE connection_id = ? AND at <= (
		SELECT at FROM forge_deliveries WHERE connection_id = ? ORDER BY at DESC LIMIT 1 OFFSET ?)`, connection, connection, keep)
	return err
}

// PruneDeliveries forgets deliveries older than maxAge.
func (s *Store) PruneDeliveries(ctx context.Context, maxAge time.Duration) (int64, error) {
	res, err := s.db.Exec(ctx, `DELETE FROM forge_deliveries WHERE at < ?`, ms(s.now().Add(-maxAge)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func affected(res sql.Result, err error, what string) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func isUnique(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "23505") || strings.Contains(msg, "duplicate key")
}

// CleanAddress removes any user name and password from a Git address and
// splits it into the field it belongs in. A local path has no address to
// show people.
func CleanAddress(raw string) (https, ssh string) {
	u, err := url.Parse(raw)
	if err == nil && u.Host != "" {
		u.User = nil
		u.RawQuery, u.Fragment = "", ""
		switch u.Scheme {
		case "http", "https":
			return u.String(), ""
		case "ssh":
			return "", u.String()
		}
		return "", ""
	}
	// scp-like: git@host:path
	if i := strings.Index(raw, "@"); i > 0 && strings.Contains(raw[i:], ":") && !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "/") {
		return "", raw[i+1:]
	}
	return "", ""
}

// IsUnique reports whether a database error is a violated uniqueness rule.
func IsUnique(err error) bool { return isUnique(err) }

// DB returns the database the store works on.
func (s *Store) DB() DB { return s.db }
