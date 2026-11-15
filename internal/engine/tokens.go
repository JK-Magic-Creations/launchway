package engine

// API tokens limited to a role and to projects (schema 13).
//
// A limited token's hash is stored as "v2:" and the SHA-256, which servers
// older than schema 13 never look up: during a rollback such a token is
// refused, rather than accepted with its owner's full rights. A token with
// no limits keeps the plain hash, so it works on older servers too.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// limitedPrefix marks the hash of a limited token.
const limitedPrefix = "v2:"

// TokensLimited reports whether this server knows limited tokens.
func (e *Engine) TokensLimited() bool { return e.knows(schemaTriggers) }

// CreateLimitedToken stores a token that may be limited to a role and to
// projects.
func (e *Engine) CreateLimitedToken(t APIToken, hash string) error {
	if t.Role == "" && len(t.Projects) == 0 {
		return e.CreateToken(t, hash)
	}
	if !e.TokensLimited() {
		return fmt.Errorf("%w: this server does not know limited tokens (schema %d)", ErrConflict, schemaTriggers)
	}
	projects, _ := json.Marshal(t.Projects)
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`INSERT INTO api_tokens(id, hash, username, name, created_at, expires_at, role, projects) VALUES(?,?,?,?,?,?,?,?)`,
		t.ID, limitedPrefix+hash, t.User, t.Name, t.CreatedAt, t.ExpiresAt, t.Role, string(projects))
	if isConflict(err) {
		return fmt.Errorf("%w: you already have a token called %q", ErrConflict, t.Name)
	}
	return err
}

const tokenCols13 = `t.id, t.name, t.username, t.created_at, t.expires_at, t.last_used, t.role, t.projects, t.last_used_from`

func scanToken13(sc interface{ Scan(...any) error }, extra ...any) (*APIToken, error) {
	t := &APIToken{}
	var projects string
	if err := sc.Scan(append([]any{&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed, &t.Role, &projects, &t.LastUsedFrom}, extra...)...); err != nil {
		return nil, err
	}
	if projects != "" {
		_ = json.Unmarshal([]byte(projects), &t.Projects)
	}
	return t, nil
}

// LookupToken finds a token by the hash of its value, limited or not, with
// its owner's groups as of their latest sign-in.
func (e *Engine) LookupToken(hash string) (*APIToken, []string, error) {
	if !e.TokensLimited() {
		return e.TokenByHash(hash)
	}
	var groups, name, email sql.NullString
	row := e.db.QueryRow(`SELECT `+tokenCols13+`, u.group_list, u.display_name, u.email
		FROM api_tokens t LEFT JOIN users u ON u.username = t.username WHERE t.hash IN (?, ?)`, hash, limitedPrefix+hash)
	t, err := scanToken13(row, &groups, &name, &email)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("%w: token", ErrNotFound)
	}
	if err != nil {
		return nil, nil, err
	}
	t.Owner = Profile{Name: name.String, Email: email.String}
	return t, decodeGroups(groups), nil
}

// TouchTokenFrom records that a token was used at now from an address,
// unless that was already recorded since notBefore from the same address.
func (e *Engine) TouchTokenFrom(id string, now, notBefore int64, addr string) error {
	if !e.TokensLimited() {
		return e.TouchToken(id, now, notBefore)
	}
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`UPDATE api_tokens SET last_used = ?, last_used_from = ? WHERE id = ? AND (last_used < ? OR last_used_from <> ?)`,
		now, addr, id, notBefore, addr)
	return err
}

// TokenList lists a person's tokens, or with user "" everyone's, newest
// first.
func (e *Engine) TokenList(user string) ([]APIToken, error) {
	if !e.TokensLimited() {
		if user == "" {
			return nil, fmt.Errorf("%w: this server cannot list everyone's tokens (schema %d)", ErrConflict, schemaTriggers)
		}
		return e.Tokens(user)
	}
	q, args := `SELECT `+tokenCols13+` FROM api_tokens t`, []any{}
	if user != "" {
		q += ` WHERE t.username = ?`
		args = append(args, user)
	}
	rows, err := e.db.Query(q+` ORDER BY t.created_at DESC, t.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanToken13(rows)
		if err != nil {
			return nil, err
		}
		if user == "" {
			t.OwnerName = t.User
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// RevokeAnyToken deletes anyone's token, for an admin.
func (e *Engine) RevokeAnyToken(id string) (*APIToken, error) {
	unlock := e.writeLock()
	defer unlock()
	t := &APIToken{}
	err := e.db.QueryRow(`DELETE FROM api_tokens WHERE id = ? RETURNING id, name, username, created_at, expires_at, last_used`, id).
		Scan(&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: token %s", ErrNotFound, id)
	}
	return t, err
}

// IsLimitedHash reports whether a stored hash is that of a limited token.
func IsLimitedHash(h string) bool { return strings.HasPrefix(h, limitedPrefix) }
