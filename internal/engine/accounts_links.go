package engine

import (
	"database/sql"
	"errors"
	"fmt"
)

// One-time links and the one-time setup code of a new installation.

// Kinds of one-time link.
const (
	LinkInvite = "invite" // welcomes a new person, who chooses a password
	LinkReset  = "reset"  // lets a person who is locked out choose a new password and second factor
)

// Link is a one-time link, as stored (the secret part is kept only as a hash).
type Link struct {
	Hash      string
	Username  string
	Kind      string
	Role      string
	CreatedAt int64
	CreatedBy string
	ExpiresAt int64
	UsedAt    int64
}

// CreateLink stores a link and withdraws any earlier unused link of the same
// person and kind, so only the newest works.
func (e *Engine) CreateLink(l Link) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM local_links WHERE username = ? AND kind = ? AND used_at = 0`, l.Username, l.Kind); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO local_links(token_hash, username, kind, role, created_at, created_by, expires_at) VALUES(?,?,?,?,?,?,?)`,
		l.Hash, l.Username, l.Kind, l.Role, l.CreatedAt, l.CreatedBy, l.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit()
}

// LinkByHash returns a link that is unused and not expired, or ErrNotFound
// (the same answer for unknown, used and expired, so a probe learns nothing).
func (e *Engine) LinkByHash(hash string, now int64) (*Link, error) {
	l := &Link{}
	err := e.db.QueryRow(`SELECT token_hash, username, kind, role, created_at, created_by, expires_at, used_at FROM local_links
		WHERE token_hash = ? AND used_at = 0 AND expires_at > ?`, hash, now).
		Scan(&l.Hash, &l.Username, &l.Kind, &l.Role, &l.CreatedAt, &l.CreatedBy, &l.ExpiresAt, &l.UsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: link", ErrNotFound)
	}
	return l, err
}

// UseLink marks a link used; false if it was already used or has expired.
func (e *Engine) UseLink(hash string, now int64) (bool, error) {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`UPDATE local_links SET used_at = ? WHERE token_hash = ? AND used_at = 0 AND expires_at > ?`, now, hash, now)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetupState is the one-time code that starts an installation.
type SetupState struct {
	CodeHash  string
	IssuedAt  int64
	ExpiresAt int64
	UsedAt    int64
	Source    string // "file", "generated" or "console"
}

// SetupCodeState returns the current setup code's state, or nil if none was ever issued.
func (e *Engine) SetupCodeState() (*SetupState, error) {
	s := &SetupState{}
	err := e.db.QueryRow(`SELECT code_hash, issued_at, expires_at, used_at, source FROM setup_state WHERE id = 1`).
		Scan(&s.CodeHash, &s.IssuedAt, &s.ExpiresAt, &s.UsedAt, &s.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// IssueSetupCode records a setup code (as a hash), replacing an earlier one
// that has not been used. It refuses once setup has been completed.
func (e *Engine) IssueSetupCode(hash string, now, expires int64, source string) error {
	unlock := e.writeLock()
	defer unlock()
	var used int64
	err := e.db.QueryRow(`SELECT used_at FROM setup_state WHERE id = 1`).Scan(&used)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = e.db.Exec(`INSERT INTO setup_state(id, code_hash, issued_at, expires_at, source) VALUES(1,?,?,?,?)`, hash, now, expires, source)
		return err
	case err != nil:
		return err
	case used != 0:
		return fmt.Errorf("%w: setup was already completed", ErrConflict)
	}
	_, err = e.db.Exec(`UPDATE setup_state SET code_hash = ?, issued_at = ?, expires_at = ?, source = ? WHERE id = 1`, hash, now, expires, source)
	return err
}

// ErrSetupCode is returned when the setup code is wrong, used or expired. One
// error for all three, so a guess learns nothing.
var ErrSetupCode = errors.New("the setup code is not valid")

// CompleteSetup creates the first administrator in one transaction with
// spending the setup code: if the code does not match the unused, unexpired
// one, or an account already exists, nothing is created.
func (e *Engine) CompleteSetup(codeHash string, a *LocalAccount, recoveryHashes []string, now int64) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM local_accounts`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: setup was already completed", ErrConflict)
	}
	res, err := tx.Exec(`UPDATE setup_state SET used_at = ? WHERE id = 1 AND code_hash = ? AND used_at = 0 AND expires_at > ?`, now, codeHash, now)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c != 1 {
		return ErrSetupCode
	}
	if err := insertLocalAccount(tx, a); err != nil {
		return err
	}
	for _, h := range recoveryHashes {
		if _, err := tx.Exec(`INSERT INTO local_recovery_codes(username, code_hash) VALUES(?,?)`, a.Username, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}
