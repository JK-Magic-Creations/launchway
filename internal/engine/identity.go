package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// People, their sessions and API tokens, and the audit trail. The policy
// (who may do what, and for how long a session lasts) is the API's; this
// only stores. Times are Unix milliseconds chosen by the caller.
//
// The people, sessions and tokens are stored by internal/store, which keeps
// each Base's apart (docs/development/the-wall.md). The methods here are the
// ones that existed before Bases: they act for the default Base, which is the
// only Base an installation without the multi_base switch has. A caller that
// knows its Base uses Identity() with the scope the request resolved.

// Session is a signed-in browser.
type Session = store.Session

// Profile is what the provider said about a person at their latest sign-in,
// beyond their username: used to author the commits they save. Either may
// be empty.
type Profile = store.Profile

// APIToken describes a token. The token itself is never stored.
type APIToken = store.APIToken

// SignIn records a person's groups and profile as their provider gave them
// now, and a new session for them. Sessions that ended before staleBefore
// (idle) or now (absolute) are forgotten in passing.
func (e *Engine) SignIn(user string, groups []string, profile Profile, sessionHash string, now, expiresAt, staleBefore int64) error {
	return e.ident().SignIn(e.scope0(), user, groups, profile, sessionHash, now, expiresAt, staleBefore)
}

// SessionByHash looks a session up. It returns ErrNotFound if there is none.
func (e *Engine) SessionByHash(hash string) (*Session, error) {
	return e.ident().SessionByHash(e.scope0(), hash)
}

// TouchSession records that a session was used at now, unless that was
// already recorded since notBefore. Every server does the same, so the
// condition keeps writes to about one per session per interval.
func (e *Engine) TouchSession(hash string, now, notBefore int64) error {
	return e.ident().TouchSession(e.scope0(), hash, now, notBefore)
}

// EndSession deletes a session and returns whose it was ("" if there was
// none).
func (e *Engine) EndSession(hash string) (string, error) {
	return e.ident().EndSession(e.scope0(), hash)
}

// CreateToken stores a token under the hash of its value. A person's
// tokens have different names.
func (e *Engine) CreateToken(t APIToken, hash string) error {
	return e.ident().CreateToken(e.scope0(), t, hash)
}

// TokenByHash looks a token up by the hash of its value, with its owner's
// groups as of their latest sign-in.
func (e *Engine) TokenByHash(hash string) (*APIToken, []string, error) {
	return e.ident().TokenByHash(e.scope0(), hash, hash)
}

// LastSignIn returns a person's groups and profile as of their latest
// sign-in; ErrNotFound if they never signed in.
func (e *Engine) LastSignIn(user string) ([]string, Profile, error) {
	return e.ident().LastSignIn(e.scope0(), user)
}

// TouchToken records that a token was used at now, unless that was already
// recorded since notBefore.
func (e *Engine) TouchToken(id string, now, notBefore int64) error {
	return e.ident().TouchToken(e.scope0(), id, now, notBefore, "")
}

// Tokens lists a person's tokens, newest first.
func (e *Engine) Tokens(user string) ([]APIToken, error) {
	if user == "" {
		return nil, fmt.Errorf("%w: a person's tokens need a person", ErrInvalid)
	}
	return e.ident().Tokens(e.scope0(), user)
}

// RevokeToken deletes one of a person's tokens. Someone else's token is
// reported as not found.
func (e *Engine) RevokeToken(user, id string) (*APIToken, error) {
	return e.ident().RevokeToken(e.scope0(), user, id)
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
	// TokenID and TokenName name the API token the request was authenticated
	// with; "" when it was not a token. Never the secret.
	TokenID   string `json:"token_id,omitempty"`
	TokenName string `json:"token_name,omitempty"`
	// RequestID is the request that caused the entry. It is not stored in the
	// table and not in the API's view: only the audit stream carries it.
	RequestID string `json:"-"`
}

// Audit appends an entry to the audit trail. Nothing changes or deletes
// entries.
func (e *Engine) Audit(a AuditEntry) error {
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO audit(at, username, source, action, project, target, outcome, detail, token_id, token_name) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		a.At, a.User, a.Source, a.Action, a.Project, a.Target, a.Outcome, a.Detail, a.TokenID, a.TokenName)
	if err == nil {
		e.emitAudit(a)
	}
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
	q := `SELECT id, at, username, source, action, project, target, outcome, detail, token_id, token_name FROM audit`
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
		if err := rows.Scan(&a.ID, &a.At, &a.User, &a.Source, &a.Action, &a.Project, &a.Target, &a.Outcome, &a.Detail, &a.TokenID, &a.TokenName); err != nil {
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
