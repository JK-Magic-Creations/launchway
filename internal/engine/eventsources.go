package engine

// Event sources (schema 49): incoming webhook sources made in the product,
// with their sealed signing secret, an optional schema, a mapping of the body
// to the pipeline's inputs, a filter, a dedupe key and a debounce or batch
// window; and the memory these need (keys seen, windows open).
//
// The engine keeps these rows; it never opens a secret (the API seals it and
// opens it) and never judges a body (internal/eventexpr does, called from
// internal/source and internal/api).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// Limits on event sources.
const (
	MaxEventSourcesPerProject = 50
	DefaultMaxBody            = 64 << 10
	MaxBodyLimit              = 1 << 20
	DefaultRatePerMinute      = 60
	MaxWindowSeconds          = 3600
	// DedupeMemory is how long a dedupe key is remembered.
	DedupeMemory = 7 * 24 * time.Hour
)

var eventSourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidEventSourceName reports whether name can name an event source.
func ValidEventSourceName(name string) bool { return eventSourceNameRe.MatchString(name) }

// EventSource is one incoming source of a project. The secret is not part of
// it: the sealed text is read by SourceSecret, and only the API opens it.
type EventSource struct {
	ID            string            `json:"id"`
	Project       string            `json:"project"`
	Name          string            `json:"name"`
	HasSecret     bool              `json:"has_secret"`
	Schema        string            `json:"schema,omitempty"`
	Mapping       map[string]string `json:"mapping"`
	Filter        string            `json:"filter,omitempty"`
	DedupeKey     string            `json:"dedupe_key,omitempty"`
	Ref           string            `json:"ref,omitempty"`
	DebounceSecs  int               `json:"debounce_seconds"`
	BatchSecs     int               `json:"batch_seconds"`
	MaxBody       int               `json:"max_body"`
	RatePerMinute int               `json:"rate_per_minute"`
	Enabled       bool              `json:"enabled"`
	CreatedAt     int64             `json:"created_at"`
	CreatedBy     string            `json:"created_by"`
	LastReceived  int64             `json:"last_received_at"`
}

const eventSourceColumns = `id, project, name, secret_sealed, schema_json, mapping, filter_expr, dedupe_expr, ref, debounce_secs, batch_secs, max_body, rate_per_min, enabled, created_at, created_by, last_received_at`

func scanEventSource(row interface{ Scan(...any) error }) (*EventSource, string, error) {
	var s EventSource
	var sealed, mapping string
	var enabled int
	if err := row.Scan(&s.ID, &s.Project, &s.Name, &sealed, &s.Schema, &mapping, &s.Filter, &s.DedupeKey, &s.Ref, &s.DebounceSecs, &s.BatchSecs,
		&s.MaxBody, &s.RatePerMinute, &enabled, &s.CreatedAt, &s.CreatedBy, &s.LastReceived); err != nil {
		return nil, "", err
	}
	s.Enabled = enabled == 1
	s.HasSecret = sealed != ""
	s.Mapping = map[string]string{}
	_ = json.Unmarshal([]byte(mapping), &s.Mapping)
	return &s, sealed, nil
}

// CheckEventSource says what is wrong with a source's settings, in words.
func CheckEventSource(s *EventSource) error {
	switch {
	case !ValidEventSourceName(s.Name):
		return fmt.Errorf("%w: a source's name is lower-case letters, digits and hyphens, up to 63", ErrInvalid)
	case s.DebounceSecs < 0 || s.DebounceSecs > MaxWindowSeconds || s.BatchSecs < 0 || s.BatchSecs > MaxWindowSeconds:
		return fmt.Errorf("%w: a window is 0 to %d seconds", ErrInvalid, MaxWindowSeconds)
	case s.DebounceSecs > 0 && s.BatchSecs > 0:
		return fmt.Errorf("%w: a source debounces or batches, not both", ErrInvalid)
	case s.MaxBody < 1 || s.MaxBody > MaxBodyLimit:
		return fmt.Errorf("%w: the largest body is 1 to %d bytes", ErrInvalid, MaxBodyLimit)
	case s.RatePerMinute < 1 || s.RatePerMinute > 6000:
		return fmt.Errorf("%w: the rate is 1 to 6000 calls a minute", ErrInvalid)
	case len(s.Mapping) > 50:
		return fmt.Errorf("%w: at most 50 inputs are mapped", ErrInvalid)
	}
	return nil
}

// SecretSealer seals a source's signing secret for its row (internal/sealing's
// box of event source secrets).
type SecretSealer interface {
	Seal(ctx context.Context, row string, plain []byte) (string, error)
}

// CreateEventSource makes a source with the signing secret given, which is
// sealed here and never kept in the clear. It returns the source.
func (e *Engine) CreateEventSource(ctx context.Context, s EventSource, secret string, seal SecretSealer) (*EventSource, error) {
	if !e.knows(schemaEvents) {
		return nil, fmt.Errorf("%w: this server does not know event sources (schema %d)", ErrConflict, schemaEvents)
	}
	if s.MaxBody == 0 {
		s.MaxBody = DefaultMaxBody
	}
	if s.RatePerMinute == 0 {
		s.RatePerMinute = DefaultRatePerMinute
	}
	if err := CheckEventSource(&s); err != nil {
		return nil, err
	}
	if s.ID == "" {
		s.ID = ids.New("evs")
	}
	// Sealed before the write lock is taken: sealing may write too.
	secretSealed, err := seal.Seal(ctx, s.ID, []byte(secret))
	if err != nil {
		return nil, err
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM event_sources WHERE project = ?`, s.Project).Scan(&n); err != nil {
		return nil, err
	}
	if n >= MaxEventSourcesPerProject {
		return nil, fmt.Errorf("%w: a project has at most %d event sources", ErrConflict, MaxEventSourcesPerProject)
	}
	s.CreatedAt = e.now().UnixMilli()
	mapping, _ := json.Marshal(s.Mapping)
	if _, err := tx.Exec(`INSERT INTO event_sources(id, project, name, secret_sealed, schema_json, mapping, filter_expr, dedupe_expr, ref, debounce_secs, batch_secs, max_body, rate_per_min, enabled, created_at, created_by, last_received_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Project, s.Name, secretSealed, s.Schema, string(mapping), s.Filter, s.DedupeKey, s.Ref, s.DebounceSecs, s.BatchSecs, s.MaxBody, s.RatePerMinute,
		1, s.CreatedAt, s.CreatedBy, 0); err != nil {
		if isUnique(err) {
			return nil, fmt.Errorf("%w: project %s already has an event source called %s", ErrConflict, s.Project, s.Name)
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.HasSecret, s.Enabled = true, true
	return &s, nil
}

func isUnique(err error) bool {
	m := strings.ToLower(err.Error())
	return strings.Contains(m, "unique") || strings.Contains(m, "duplicate key")
}

// EventSources lists a project's sources.
func (e *Engine) EventSources(project string) ([]EventSource, error) {
	if !e.knows(schemaEvents) {
		return []EventSource{}, nil
	}
	rows, err := e.db.Query(`SELECT `+eventSourceColumns+` FROM event_sources WHERE project = ? ORDER BY name`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventSource{}
	for rows.Next() {
		s, _, err := scanEventSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// EventSourceNamed returns a source and its sealed secret.
func (e *Engine) EventSourceNamed(project, name string) (*EventSource, string, error) {
	if !e.knows(schemaEvents) {
		return nil, "", ErrNotFound
	}
	s, sealed, err := scanEventSource(e.db.QueryRow(`SELECT `+eventSourceColumns+` FROM event_sources WHERE project = ? AND name = ?`, project, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("%w: event source %s of %s", ErrNotFound, name, project)
	}
	return s, sealed, err
}

// ChangeEventSource replaces a source's settings (not its name, project or
// secret); a non-empty newSecret replaces the secret, sealed here.
func (e *Engine) ChangeEventSource(ctx context.Context, s EventSource, newSecret string, seal SecretSealer) error {
	if err := CheckEventSource(&s); err != nil {
		return err
	}
	mapping, _ := json.Marshal(s.Mapping)
	en := 0
	if s.Enabled {
		en = 1
	}
	sealed := ""
	if newSecret != "" {
		var err error
		if sealed, err = seal.Seal(ctx, s.ID, []byte(newSecret)); err != nil {
			return err
		}
	}
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`UPDATE event_sources SET schema_json = ?, mapping = ?, filter_expr = ?, dedupe_expr = ?, ref = ?, debounce_secs = ?, batch_secs = ?, max_body = ?, rate_per_min = ?, enabled = ? WHERE project = ? AND name = ?`,
		s.Schema, string(mapping), s.Filter, s.DedupeKey, s.Ref, s.DebounceSecs, s.BatchSecs, s.MaxBody, s.RatePerMinute, en, s.Project, s.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: event source %s of %s", ErrNotFound, s.Name, s.Project)
	}
	if sealed != "" {
		_, err = e.db.Exec(`UPDATE event_sources SET secret_sealed = ? WHERE id = ?`, sealed, s.ID)
	}
	return err
}

// DeleteEventSource removes a source and what it remembers.
func (e *Engine) DeleteEventSource(project, name string) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	if err := tx.QueryRow(`SELECT id FROM event_sources WHERE project = ? AND name = ?`, project, name).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: event source %s of %s", ErrNotFound, name, project)
		}
		return err
	}
	for _, q := range []string{`DELETE FROM event_sources WHERE id = ?`, `DELETE FROM event_dedupe WHERE scope IN ('src:' || ?, 'sig:' || ?)`, `DELETE FROM event_windows WHERE source = ?`} {
		args := []any{id}
		if strings.Count(q, "?") == 2 {
			args = []any{id, id}
		}
		if strings.Contains(q, "event_windows") {
			args = []any{name}
		}
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TouchEventSource records that a source was just called.
func (e *Engine) TouchEventSource(id string) {
	unlock := e.writeLock()
	defer unlock()
	_, _ = e.db.Exec(`UPDATE event_sources SET last_received_at = ? WHERE id = ?`, e.now().UnixMilli(), id)
}

// ClaimKey remembers a key under a scope (a source's dedupe key, or a
// signature that may be used once). It reports claimed true when the key is
// new, or was last seen longer ago than memory; otherwise it returns the event
// that claimed it first. Exactly one of any number of concurrent callers
// claims a key.
func (e *Engine) ClaimKey(project, scope, key, eventID string, memory time.Duration) (claimed bool, first string, err error) {
	if len(key) > 500 {
		key = key[:500]
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return false, "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := e.now().UnixMilli()
	res, err := tx.Exec(`INSERT INTO event_dedupe(project, scope, dkey, at, event_id) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, project, scope, key, now, eventID)
	if err != nil {
		return false, "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, "", tx.Commit()
	}
	var at int64
	if err := tx.QueryRow(`SELECT at, event_id FROM event_dedupe WHERE project = ? AND scope = ? AND dkey = ?`, project, scope, key).Scan(&at, &first); err != nil {
		return false, "", err
	}
	if now-at > memory.Milliseconds() {
		// Forgotten: the key is new again. The compare on at makes one caller win.
		res, err := tx.Exec(`UPDATE event_dedupe SET at = ?, event_id = ? WHERE project = ? AND scope = ? AND dkey = ? AND at = ?`, now, eventID, project, scope, key, at)
		if err != nil {
			return false, "", err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return true, "", tx.Commit()
		}
	}
	return false, first, nil
}

// SetClaimEvent tells a claimed key which event holds it (the event is
// recorded after the key is claimed).
func (e *Engine) SetClaimEvent(project, scope, key, eventID string) {
	unlock := e.writeLock()
	defer unlock()
	_, _ = e.db.Exec(`UPDATE event_dedupe SET event_id = ? WHERE project = ? AND scope = ? AND dkey = ?`, eventID, project, scope, key)
}

// PruneEventMemory forgets keys and signatures older than their memory.
func (e *Engine) PruneEventMemory() {
	unlock := e.writeLock()
	defer unlock()
	now := e.now()
	_, _ = e.db.Exec(`DELETE FROM event_dedupe WHERE scope LIKE 'sig:%' AND at < ?`, now.Add(-time.Hour).UnixMilli())
	_, _ = e.db.Exec(`DELETE FROM event_dedupe WHERE at < ?`, now.Add(-DedupeMemory).UnixMilli())
	_, _ = e.db.Exec(`DELETE FROM event_log WHERE at < ? AND id NOT IN (SELECT event_id FROM event_decisions WHERE run_id <> '')`, now.Add(-30*24*time.Hour).UnixMilli())
}

// ---- windows ----

// EventWindow is an open debounce or batch window: the events it holds back
// start one run when it closes.
type EventWindow struct {
	Project, Source, Mode string
	OpenedAt, DueAt       int64
	Count                 int
	EventID               string // the newest event, whose inputs are used
	Inputs                map[string]string
}

// AddToWindow puts an event into its source's window, opening it if none is
// open. A debounce pushes the due time back to window after now; a batch
// keeps the due time of the first event. It returns the window and the event
// that was newest before (to mark it as folded in), "" if the window is new.
func (e *Engine) AddToWindow(project, source, mode string, window time.Duration, eventID string, inputs map[string]string) (*EventWindow, string, error) {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = tx.Rollback() }()
	now := e.now().UnixMilli()
	in, _ := json.Marshal(inputs)
	var w EventWindow
	var raw string
	err = tx.QueryRow(`SELECT opened_at, due_at, count, event_id, inputs FROM event_windows WHERE project = ? AND source = ?`, project, source).Scan(&w.OpenedAt, &w.DueAt, &w.Count, &w.EventID, &raw)
	prev := ""
	switch {
	case errors.Is(err, sql.ErrNoRows):
		w = EventWindow{OpenedAt: now, DueAt: now + window.Milliseconds(), Count: 1, EventID: eventID}
		if _, err := tx.Exec(`INSERT INTO event_windows(project, source, mode, opened_at, due_at, count, event_id, inputs) VALUES(?,?,?,?,?,?,?,?)`,
			project, source, mode, w.OpenedAt, w.DueAt, 1, eventID, string(in)); err != nil {
			return nil, "", err
		}
	case err != nil:
		return nil, "", err
	default:
		prev = w.EventID
		w.Count++
		if mode == "debounce" {
			w.DueAt = now + window.Milliseconds()
		}
		w.EventID = eventID
		if _, err := tx.Exec(`UPDATE event_windows SET due_at = ?, count = ?, event_id = ?, inputs = ? WHERE project = ? AND source = ?`, w.DueAt, w.Count, eventID, string(in), project, source); err != nil {
			return nil, "", err
		}
	}
	w.Project, w.Source, w.Mode = project, source, mode
	return &w, prev, tx.Commit()
}

// DueWindows lists the windows that have closed.
func (e *Engine) DueWindows() ([]EventWindow, error) {
	if !e.knows(schemaEvents) {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT project, source, mode, opened_at, due_at, count, event_id, inputs FROM event_windows WHERE due_at <= ?`, e.now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventWindow
	for rows.Next() {
		var w EventWindow
		var raw string
		if err := rows.Scan(&w.Project, &w.Source, &w.Mode, &w.OpenedAt, &w.DueAt, &w.Count, &w.EventID, &raw); err != nil {
			return nil, err
		}
		w.Inputs = map[string]string{}
		_ = json.Unmarshal([]byte(raw), &w.Inputs)
		out = append(out, w)
	}
	return out, rows.Err()
}

// ClaimWindow takes a closed window so that one server starts its run. It
// reports whether this call took it.
func (e *Engine) ClaimWindow(w EventWindow) (bool, error) {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`DELETE FROM event_windows WHERE project = ? AND source = ? AND due_at = ? AND count = ?`, w.Project, w.Source, w.DueAt, w.Count)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ForgetKey drops a claimed key (the run it was claimed for could not start,
// so a retry must not be called a duplicate).
func (e *Engine) ForgetKey(project, scope, key string) {
	unlock := e.writeLock()
	defer unlock()
	_, _ = e.db.Exec(`DELETE FROM event_dedupe WHERE project = ? AND scope = ? AND dkey = ?`, project, scope, key)
}
