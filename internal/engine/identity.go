package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// People, their sessions and API tokens, and the audit trail. The policy
// (who may do what, and for how long a session lasts) is the API's; this
// only stores. Times are Unix milliseconds chosen by the caller.

// Session is a signed-in browser.
type Session struct {
	User      string
	Groups    []string // from the person's latest sign-in
	Profile   Profile  // likewise
	CreatedAt int64
	LastSeen  int64
	ExpiresAt int64
}

// Profile is what the provider said about a person at their latest sign-in,
// beyond their username: used to author the commits they save. Either may
// be empty.
type Profile struct {
	Name  string // display name
	Email string // only an address the provider has not said is unverified
}

// SignIn records a person's groups and profile as their provider gave them
// now, and a new session for them. Sessions that ended before staleBefore
// (idle) or now (absolute) are forgotten in passing.
func (e *Engine) SignIn(user string, groups []string, profile Profile, sessionHash string, now, expiresAt, staleBefore int64) error {
	g, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO users(username, group_list, last_login, display_name, email) VALUES(?,?,?,?,?)
		ON CONFLICT(username) DO UPDATE SET group_list = excluded.group_list, last_login = excluded.last_login,
			display_name = excluded.display_name, email = excluded.email`,
		user, string(g), now, profile.Name, profile.Email); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO sessions(id_hash, username, created_at, last_seen, expires_at) VALUES(?,?,?,?,?)`,
		sessionHash, user, now, now, expiresAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE expires_at <= ? OR last_seen < ?`, now, staleBefore); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionByHash looks a session up. It returns ErrNotFound if there is none.
func (e *Engine) SessionByHash(hash string) (*Session, error) {
	s := &Session{}
	var groups, name, email sql.NullString
	err := e.db.QueryRow(`SELECT s.username, u.group_list, u.display_name, u.email, s.created_at, s.last_seen, s.expires_at
		FROM sessions s LEFT JOIN users u ON u.username = s.username WHERE s.id_hash = ?`, hash).
		Scan(&s.User, &groups, &name, &email, &s.CreatedAt, &s.LastSeen, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: session", ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	s.Groups = decodeGroups(groups)
	s.Profile = Profile{Name: name.String, Email: email.String}
	return s, nil
}

// TouchSession records that a session was used at now, unless that was
// already recorded since notBefore. Every server does the same, so the
// condition keeps writes to about one per session per interval.
func (e *Engine) TouchSession(hash string, now, notBefore int64) error {
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`UPDATE sessions SET last_seen = ? WHERE id_hash = ? AND last_seen < ?`, now, hash, notBefore)
	return err
}

// EndSession deletes a session and returns whose it was ("" if there was
// none).
func (e *Engine) EndSession(hash string) (string, error) {
	unlock := e.writeLock()
	defer unlock()
	var user string
	err := e.db.QueryRow(`DELETE FROM sessions WHERE id_hash = ? RETURNING username`, hash).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return user, err
}

func decodeGroups(s sql.NullString) []string {
	out := []string{}
	if s.Valid {
		_ = json.Unmarshal([]byte(s.String), &out)
	}
	return out
}

// APIToken describes a token. The token itself is never stored.
type APIToken struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	User      string  `json:"-"`
	Owner     Profile `json:"-"` // the owner's profile as of their latest sign-in
	CreatedAt int64   `json:"created_at"`
	ExpiresAt int64   `json:"expires_at"`
	LastUsed  int64   `json:"last_used"`
	// Schema 13: the role the token is limited to ("" for its owner's),
	// the project patterns it is limited to (none for every project), and
	// the address it was last used from.
	Role         string   `json:"role,omitempty"`
	Projects     []string `json:"projects,omitempty"`
	LastUsedFrom string   `json:"last_used_from,omitempty"`
	// Owner names whose token it is, in lists only admins see.
	OwnerName string `json:"user,omitempty"`
}

// CreateToken stores a token under the hash of its value. A person's
// tokens have different names.
func (e *Engine) CreateToken(t APIToken, hash string) error {
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO api_tokens(id, hash, username, name, created_at, expires_at) VALUES(?,?,?,?,?,?)`,
		t.ID, hash, t.User, t.Name, t.CreatedAt, t.ExpiresAt)
	if isConflict(err) {
		return fmt.Errorf("%w: you already have a token called %q", ErrConflict, t.Name)
	}
	return err
}

// TokenByHash looks a token up by the hash of its value, with its owner's
// groups as of their latest sign-in.
func (e *Engine) TokenByHash(hash string) (*APIToken, []string, error) {
	t := &APIToken{}
	var groups, name, email sql.NullString
	err := e.db.QueryRow(`SELECT t.id, t.name, t.username, t.created_at, t.expires_at, t.last_used, u.group_list, u.display_name, u.email
		FROM api_tokens t LEFT JOIN users u ON u.username = t.username WHERE t.hash = ?`, hash).
		Scan(&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed, &groups, &name, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("%w: token", ErrNotFound)
	}
	if err != nil {
		return nil, nil, err
	}
	t.Owner = Profile{Name: name.String, Email: email.String}
	return t, decodeGroups(groups), nil
}

// TouchToken records that a token was used at now, unless that was already
// recorded since notBefore.
func (e *Engine) TouchToken(id string, now, notBefore int64) error {
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`UPDATE api_tokens SET last_used = ? WHERE id = ? AND last_used < ?`, now, id, notBefore)
	return err
}

// Tokens lists a person's tokens, newest first.
func (e *Engine) Tokens(user string) ([]APIToken, error) {
	rows, err := e.db.Query(`SELECT id, name, username, created_at, expires_at, last_used FROM api_tokens
		WHERE username = ? ORDER BY created_at DESC, id DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken deletes one of a person's tokens. Someone else's token is
// reported as not found.
func (e *Engine) RevokeToken(user, id string) (*APIToken, error) {
	unlock := e.writeLock()
	defer unlock()
	t := &APIToken{}
	err := e.db.QueryRow(`DELETE FROM api_tokens WHERE id = ? AND username = ?
		RETURNING id, name, username, created_at, expires_at, last_used`, id, user).
		Scan(&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: token %s", ErrNotFound, id)
	}
	return t, err
}

// AuditEntry is one line of the audit trail.
type AuditEntry struct {
	ID      int64  `json:"id"`
	At      int64  `json:"at"`
	User    string `json:"user"`
	Source  string `json:"source"`  // the client's address
	Action  string `json:"action"`  // such as run.start or auth.sign_in
	Project string `json:"project"` // "" if none
	Target  string `json:"target"`  // a run, run/step or token name
	Outcome string `json:"outcome"` // ok, denied or failed
	Detail  string `json:"detail"`
}

// Audit appends an entry to the audit trail. Nothing changes or deletes
// entries.
func (e *Engine) Audit(a AuditEntry) error {
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO audit(at, username, source, action, project, target, outcome, detail) VALUES(?,?,?,?,?,?,?,?)`,
		a.At, a.User, a.Source, a.Action, a.Project, a.Target, a.Outcome, a.Detail)
	return err
}

// AuditFilter narrows the audit trail. Zero values match everything.
type AuditFilter struct {
	User, Project, Action string
	From, To              int64 // At in [From, To)
	Before                int64 // only entries with a smaller ID: the next page
	Limit                 int
}

// AuditLog returns entries newest first.
func (e *Engine) AuditLog(f AuditFilter) ([]AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	var where []string
	var args []any
	for _, c := range []struct {
		cond string
		val  any
		set  bool
	}{
		{"username = ?", f.User, f.User != ""},
		{"project = ?", f.Project, f.Project != ""},
		{"action = ?", f.Action, f.Action != ""},
		{"at >= ?", f.From, f.From > 0},
		{"at < ?", f.To, f.To > 0},
		{"id < ?", f.Before, f.Before > 0},
	} {
		if c.set {
			where, args = append(where, c.cond), append(args, c.val)
		}
	}
	q := `SELECT id, at, username, source, action, project, target, outcome, detail FROM audit`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := e.db.Query(q+" ORDER BY id DESC LIMIT ?", append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.At, &a.User, &a.Source, &a.Action, &a.Project, &a.Target, &a.Outcome, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RunProject returns the project a run belongs to.
func (e *Engine) RunProject(runID string) (string, error) {
	var p string
	err := e.db.QueryRow(`SELECT project FROM runs WHERE id = ?`, runID).Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	return p, err
}

// ProjectsWithRuns lists every project that has ever had a run.
func (e *Engine) ProjectsWithRuns() ([]string, error) {
	rows, err := e.db.Query(`SELECT project FROM run_counters ORDER BY project`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
