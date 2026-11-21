package webhooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// ErrNotFound is a hook or delivery that does not exist.
var ErrNotFound = errors.New("no such webhook")

// Sealer keeps secrets sealed at rest, each bound to the row it belongs to
// (internal/sealing's Box for repo_webhooks.secret_sealed does). Sealing
// refuses, saying how to create a root key, when the server has none.
type Sealer interface {
	Seal(ctx context.Context, row string, plain []byte) (string, error)
	Open(ctx context.Context, row, stored string) ([]byte, error)
}

type store struct {
	db   DB
	seal Sealer
	now  func() time.Time
}

const hookCols = `id, repo_id, owner, name, url, events, secret_sealed, active, failures, disabled_reason, disabled_at, created_at, created_by`

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanHook(sc interface{ Scan(...any) error }) (Hook, string, error) {
	var h Hook
	var events, sealed string
	var active int
	var dis, created int64
	if err := sc.Scan(&h.ID, &h.RepoID, &h.Owner, &h.Name, &h.URL, &events, &sealed, &active, &h.Failures, &h.DisabledReason, &dis, &created, &h.CreatedBy); err != nil {
		return h, "", err
	}
	_ = json.Unmarshal([]byte(events), &h.Events)
	if h.Events == nil {
		h.Events = []string{}
	}
	h.Active, h.HasSecret = active != 0, sealed != ""
	h.DisabledAt, h.CreatedAt = fromMS(dis), fromMS(created)
	h.Scope = ScopeOwner
	if h.RepoID != "" {
		h.Scope = ScopeRepository
	}
	return h, sealed, nil
}

func (s *store) insertHook(ctx context.Context, h Hook, secret string) (Hook, error) {
	h.ID = ids.New("hok")
	sealed := ""
	if secret != "" {
		var err error
		if sealed, err = s.sealSecret(ctx, h.ID, secret); err != nil {
			return h, err
		}
	}
	h.CreatedAt = s.now().UTC()
	ev, _ := json.Marshal(h.Events)
	_, err := s.db.Exec(ctx, `INSERT INTO repo_webhooks(`+hookCols+`) VALUES(?,?,?,?,?,?,?,?,0,'',0,?,?)`,
		h.ID, h.RepoID, h.Owner, h.Name, h.URL, string(ev), sealed, b2i(h.Active), ms(h.CreatedAt), h.CreatedBy)
	h.HasSecret = sealed != ""
	return h, err
}

// sealSecret seals a hook's secret, bound to the hook.
func (s *store) sealSecret(ctx context.Context, id, secret string) (string, error) {
	b := []byte(secret)
	defer func() {
		for i := range b {
			b[i] = 0
		}
	}()
	return s.seal.Seal(ctx, id, b)
}

func (s *store) hook(ctx context.Context, id string) (Hook, string, error) {
	h, sealed, err := scanHook(s.db.QueryRow(ctx, `SELECT `+hookCols+` FROM repo_webhooks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return h, "", ErrNotFound
	}
	return h, sealed, err
}

func (s *store) hooks(ctx context.Context, where string, args ...any) ([]Hook, error) {
	rows, err := s.db.Query(ctx, `SELECT `+hookCols+` FROM repo_webhooks WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Hook{}
	for rows.Next() {
		h, _, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *store) saveHook(ctx context.Context, h Hook, newSecret *string) error {
	ev, _ := json.Marshal(h.Events)
	if newSecret != nil {
		sealed := ""
		if *newSecret != "" {
			var err error
			if sealed, err = s.sealSecret(ctx, h.ID, *newSecret); err != nil {
				return err
			}
		}
		if _, err := s.db.Exec(ctx, `UPDATE repo_webhooks SET secret_sealed = ? WHERE id = ?`, sealed, h.ID); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE repo_webhooks SET name = ?, url = ?, events = ?, active = ?, failures = ?, disabled_reason = ?, disabled_at = ? WHERE id = ?`,
		h.Name, h.URL, string(ev), b2i(h.Active), h.Failures, h.DisabledReason, ms(h.DisabledAt), h.ID)
	return err
}

func (s *store) deleteHook(ctx context.Context, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM repo_webhooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.Exec(ctx, `DELETE FROM repo_hook_deliveries WHERE hook_id = ?`, id)
	return err
}

const delCols = `id, hook_id, event, payload, created_at, test, state, attempts, next_attempt_at, status, duration_ms, error, response, redelivery_of`

func scanDelivery(sc interface{ Scan(...any) error }) (Delivery, error) {
	var d Delivery
	var payload string
	var created, next int64
	var test int
	if err := sc.Scan(&d.ID, &d.HookID, &d.Event, &payload, &created, &test, &d.State, &d.Attempts, &next, &d.Status, &d.DurationMS, &d.Error, &d.Response, &d.RedeliveryOf); err != nil {
		return d, err
	}
	d.Payload, d.Test = json.RawMessage(payload), test != 0
	d.CreatedAt, d.NextAttempt = fromMS(created), fromMS(next)
	return d, nil
}

func (s *store) insertDelivery(ctx context.Context, d Delivery) (Delivery, error) {
	if d.ID == "" {
		d.ID = ids.New("dlv")
	}
	d.CreatedAt = s.now().UTC()
	d.State = Pending
	if d.NextAttempt.IsZero() {
		d.NextAttempt = d.CreatedAt
	}
	_, err := s.db.Exec(ctx, `INSERT INTO repo_hook_deliveries(`+delCols+`, claimed_until) VALUES(?,?,?,?,?,?,?,0,?,0,0,'','',?,0)`,
		d.ID, d.HookID, d.Event, string(d.Payload), ms(d.CreatedAt), b2i(d.Test), d.State, ms(d.NextAttempt), d.RedeliveryOf)
	return d, err
}

func (s *store) delivery(ctx context.Context, id string) (Delivery, error) {
	d, err := scanDelivery(s.db.QueryRow(ctx, `SELECT `+delCols+` FROM repo_hook_deliveries WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

func (s *store) deliveries(ctx context.Context, hookID string, limit int) ([]Delivery, error) {
	rows, err := s.db.Query(ctx, `SELECT `+delCols+` FROM repo_hook_deliveries WHERE hook_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, hookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// due returns the IDs of deliveries waiting for an attempt.
func (s *store) due(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM repo_hook_deliveries WHERE state = ? AND next_attempt_at <= ? AND claimed_until < ? ORDER BY next_attempt_at LIMIT ?`,
		Pending, ms(now), ms(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// claim takes a delivery for a minute; only one server wins.
func (s *store) claim(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := s.db.Exec(ctx, `UPDATE repo_hook_deliveries SET claimed_until = ? WHERE id = ? AND state = ? AND claimed_until < ?`,
		ms(now.Add(time.Minute)), id, Pending, ms(now))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *store) recordAttempt(ctx context.Context, d Delivery) error {
	_, err := s.db.Exec(ctx, `UPDATE repo_hook_deliveries SET state = ?, attempts = ?, next_attempt_at = ?, claimed_until = 0, status = ?, duration_ms = ?, error = ?, response = ? WHERE id = ?`,
		d.State, d.Attempts, ms(d.NextAttempt), d.Status, d.DurationMS, d.Error, d.Response, d.ID)
	return err
}

// prune keeps the newest keep deliveries of a hook.
func (s *store) prune(ctx context.Context, hookID string, keep int) {
	_, _ = s.db.Exec(ctx, `DELETE FROM repo_hook_deliveries WHERE hook_id = ? AND state <> ? AND id NOT IN
		(SELECT id FROM (SELECT id FROM repo_hook_deliveries WHERE hook_id = ? ORDER BY created_at DESC, id DESC LIMIT ?) keep_)`, hookID, Pending, hookID, keep)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func (s *store) hookFailures(ctx context.Context, id string, delta int, reset bool) (int, error) {
	if reset {
		_, err := s.db.Exec(ctx, `UPDATE repo_webhooks SET failures = 0 WHERE id = ?`, id)
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE repo_webhooks SET failures = failures + ? WHERE id = ?`, delta, id); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRow(ctx, `SELECT failures FROM repo_webhooks WHERE id = ?`, id).Scan(&n)
	return n, err
}
