package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// Built-in accounts (schema 36): what the database keeps about people who
// sign in with a password held by this installation. The rules (the password
// policy, the throttle, who may do what) are the API's and internal/localauth's;
// this only stores, and keeps each multi-step change in one transaction.

// LocalAccount is one built-in account.
type LocalAccount struct {
	Username      string
	DisplayName   string
	Email         string
	Role          string // viewer, operator or admin
	PasswordHash  string // Argon2id; empty for an invited person who has not chosen one yet
	PasswordSetAt int64
	TOTPSecret    string // base32; always sealed under the root key in the database, in the clear only here
	TOTPConfirmed bool
	TOTPLastStep  int64
	FailedCount   int64
	LockedUntil   int64
	Disabled      bool
	BreakGlass    bool
	CreatedAt     int64
	CreatedBy     string
	LastLoginAt   int64
}

const localAccountColumns = `username, display_name, email, role, password_hash, password_set_at, totp_secret, totp_confirmed,
	totp_last_step, failed_count, locked_until, disabled, break_glass, created_at, created_by, last_login_at`

// SecretBox seals and opens the values of one registered secret column, each
// bound to its row; internal/sealing's Box satisfies it. The engine cannot
// import that package, so the server hands the box in (SetTOTPBox).
type SecretBox interface {
	Enabled() bool
	SealRow(row string, plain []byte) (string, error)
	OpenRow(row, stored string) ([]byte, error)
}

// SetTOTPBox gives the engine the box that seals local_accounts.totp_secret.
func (e *Engine) SetTOTPBox(b SecretBox) { totpBoxes.Store(e, b) }

// totpBoxes holds each engine's box (kept beside the accounts code, not in the Engine struct).
var totpBoxes sync.Map // *Engine -> SecretBox

func (e *Engine) totpBoxNow() SecretBox {
	b, _ := totpBoxes.Load(e)
	sb, _ := b.(SecretBox)
	return sb
}

// TOTPSealing says whether a second factor can be stored: a root key is in use.
func (e *Engine) TOTPSealing() bool {
	b := e.totpBoxNow()
	return b != nil && b.Enabled()
}

// sealTOTP is the stored form of a TOTP secret (empty stays empty).
func (e *Engine) sealTOTP(name, secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	b := e.totpBoxNow()
	if b == nil || !b.Enabled() {
		return "", fmt.Errorf("a second factor stores a secret: %w", keyring.ErrNoRootKey)
	}
	return b.SealRow(name, []byte(secret))
}

// openTOTP turns the stored form back into the secret, in place.
func (e *Engine) openTOTP(a *LocalAccount) error {
	if a.TOTPSecret == "" {
		return nil
	}
	b := e.totpBoxNow()
	if b == nil {
		// No box was given (the console commands, which never use the secret):
		// the sealed text stays as it is and is not a usable secret.
		a.TOTPSecret = ""
		return nil
	}
	plain, err := b.OpenRow(a.Username, a.TOTPSecret)
	if err != nil {
		return fmt.Errorf("opening the second factor of %s: %w", a.Username, err)
	}
	a.TOTPSecret = string(plain)
	return nil
}

func scanLocalAccount(row interface{ Scan(...any) error }) (*LocalAccount, error) {
	a := &LocalAccount{}
	var confirmed, disabled, bg int
	err := row.Scan(&a.Username, &a.DisplayName, &a.Email, &a.Role, &a.PasswordHash, &a.PasswordSetAt, &a.TOTPSecret, &confirmed,
		&a.TOTPLastStep, &a.FailedCount, &a.LockedUntil, &disabled, &bg, &a.CreatedAt, &a.CreatedBy, &a.LastLoginAt)
	a.TOTPConfirmed, a.Disabled, a.BreakGlass = confirmed != 0, disabled != 0, bg != 0
	return a, err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

// insertLocalAccount stores the account with its second factor already
// sealed (sealTOTP, which must run before the write lock is taken: sealing
// uses the database too).
func insertLocalAccount(q execer, a *LocalAccount, sealed string) error {
	_, err := q.Exec(`INSERT INTO local_accounts(username, display_name, email, role, password_hash, password_set_at, totp_secret, totp_confirmed,
	totp_last_step, failed_count, locked_until, disabled, break_glass, created_at, created_by, last_login_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.Username, a.DisplayName, a.Email, a.Role, a.PasswordHash, a.PasswordSetAt, sealed, b2i(a.TOTPConfirmed),
		a.TOTPLastStep, a.FailedCount, a.LockedUntil, b2i(a.Disabled), b2i(a.BreakGlass), a.CreatedAt, a.CreatedBy, a.LastLoginAt)
	return err
}

// CreateLocalAccount adds an account; ErrConflict if the name is taken.
func (e *Engine) CreateLocalAccount(a *LocalAccount) error {
	sealed, err := e.sealTOTP(a.Username, a.TOTPSecret)
	if err != nil {
		return err
	}
	unlock := e.writeLock()
	defer unlock()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM local_accounts WHERE username = ?`, a.Username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: there is already an account called %s", ErrConflict, a.Username)
	}
	return insertLocalAccount(e.db, a, sealed)
}

// LocalAccountByName returns the account, or ErrNotFound.
func (e *Engine) LocalAccountByName(name string) (*LocalAccount, error) {
	a, err := scanLocalAccount(e.db.QueryRow(`SELECT `+localAccountColumns+` FROM local_accounts WHERE username = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: account %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	return a, e.openTOTP(a)
}

// ListLocalAccounts returns every account by name.
func (e *Engine) ListLocalAccounts() ([]*LocalAccount, error) {
	rows, err := e.db.Query(`SELECT ` + localAccountColumns + ` FROM local_accounts ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LocalAccount
	for rows.Next() {
		a, err := scanLocalAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, a := range out {
		if err := e.openTOTP(a); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CountLocalAdmins counts the enabled administrators among the accounts.
func (e *Engine) CountLocalAdmins() (int, error) {
	var n int
	err := e.db.QueryRow(`SELECT count(*) FROM local_accounts WHERE role = 'admin' AND disabled = 0`).Scan(&n)
	return n, err
}

// CountLocalAccounts counts every account.
func (e *Engine) CountLocalAccounts() (int, error) {
	var n int
	err := e.db.QueryRow(`SELECT count(*) FROM local_accounts`).Scan(&n)
	return n, err
}

func (e *Engine) updateAccount(name, set string, args ...any) error {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`UPDATE local_accounts SET `+set+` WHERE username = ?`, append(args, name)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: account %s", ErrNotFound, name)
	}
	return nil
}

// SetLocalPassword stores a new password hash, clears the failure count and
// the lock, and ends every session of the account except keep (a session hash;
// "" ends them all), so a changed password signs out whoever else had it.
func (e *Engine) SetLocalPassword(name, hash string, now int64, keep string) error {
	if err := e.updateAccount(name, `password_hash = ?, password_set_at = ?, failed_count = 0, locked_until = 0`, hash, now); err != nil {
		return err
	}
	return e.endSessionsExcept(name, keep)
}

// SetLocalRole changes the role of an account.
func (e *Engine) SetLocalRole(name, role string) error {
	return e.updateAccount(name, `role = ?`, role)
}

// SetLocalDisabled turns an account off or on; turning off ends its sessions.
func (e *Engine) SetLocalDisabled(name string, disabled bool) error {
	if err := e.updateAccount(name, `disabled = ?`, b2i(disabled)); err != nil {
		return err
	}
	if disabled {
		return e.endSessionsExcept(name, "")
	}
	return nil
}

// SetLocalBreakGlass marks or unmarks an account as the break-glass administrator.
func (e *Engine) SetLocalBreakGlass(name string, on bool) error {
	return e.updateAccount(name, `break_glass = ?`, b2i(on))
}

// SetLocalProfile changes the display name and e-mail address.
func (e *Engine) SetLocalProfile(name, display, email string) error {
	return e.updateAccount(name, `display_name = ?, email = ?`, display, email)
}

// SetLocalTOTP stores a TOTP secret (unconfirmed until the person proves it
// with a code) or, with an empty secret, removes the second factor and the
// recovery codes with it.
func (e *Engine) SetLocalTOTP(name, secret string, confirmed bool) error {
	sealed, err := e.sealTOTP(name, secret)
	if err != nil {
		return err
	}
	unlock := e.writeLock()
	res, err := e.db.Exec(`UPDATE local_accounts SET totp_secret = ?, totp_confirmed = ?, totp_last_step = 0 WHERE username = ?`, sealed, b2i(confirmed), name)
	unlock()
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: account %s", ErrNotFound, name)
	}
	if secret == "" {
		return e.ReplaceRecoveryCodes(name, nil)
	}
	return nil
}

// RecordTOTPStep notes the step of a code just accepted so it cannot be used
// again; false if a code of that step or later was already used (a replay, or
// two servers racing).
func (e *Engine) RecordTOTPStep(name string, step int64) (bool, error) {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`UPDATE local_accounts SET totp_last_step = ? WHERE username = ? AND totp_last_step < ?`, step, name, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// LocalFailure counts a failed sign-in and, when lockFor says so for the new
// count, locks the account until then. It returns the count and the lock's end.
func (e *Engine) LocalFailure(name string, now time.Time, lockFor func(failures int) time.Duration) (int64, int64, error) {
	unlock := e.writeLock()
	defer unlock()
	var n int64
	err := e.db.QueryRow(`UPDATE local_accounts SET failed_count = failed_count + 1 WHERE username = ? RETURNING failed_count`, name).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	var until int64
	if d := lockFor(int(n)); d > 0 {
		until = now.Add(d).UnixMilli()
		if _, err := e.db.Exec(`UPDATE local_accounts SET locked_until = ? WHERE username = ?`, until, name); err != nil {
			return n, 0, err
		}
	}
	return n, until, nil
}

// LocalSuccess clears the failures and notes the sign-in.
func (e *Engine) LocalSuccess(name string, now int64) error {
	return e.updateAccount(name, `failed_count = 0, locked_until = 0, last_login_at = ?`, now)
}

// DeleteLocalAccount removes an account, its recovery codes, its links and its sessions.
func (e *Engine) DeleteLocalAccount(name string) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`DELETE FROM local_accounts WHERE username = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: account %s", ErrNotFound, name)
	}
	for _, q := range []string{`DELETE FROM local_recovery_codes WHERE username = ?`, `DELETE FROM local_links WHERE username = ?`, `DELETE FROM sessions WHERE username = ?`} {
		if _, err := tx.Exec(q, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (e *Engine) endSessionsExcept(name, keep string) error {
	unlock := e.writeLock()
	defer unlock()
	_, err := e.db.Exec(`DELETE FROM sessions WHERE username = ? AND id_hash <> ?`, name, keep)
	return err
}

// EndUserSessions signs a person out everywhere, and says how many sessions ended.
func (e *Engine) EndUserSessions(name string) (int64, error) {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`DELETE FROM sessions WHERE username = ?`, name)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReplaceRecoveryCodes stores the hashes of a new set of recovery codes and
// forgets the old set. nil removes them.
func (e *Engine) ReplaceRecoveryCodes(name string, hashes []string) error {
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM local_recovery_codes WHERE username = ?`, name); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(`INSERT INTO local_recovery_codes(username, code_hash) VALUES(?,?)`, name, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RecoveryCodeHashes returns the stored hashes of the unused recovery codes
// of an account, for the caller to compare a typed code with.
func (e *Engine) RecoveryCodeHashes(name string) ([]string, error) {
	rows, err := e.db.Query(`SELECT code_hash FROM local_recovery_codes WHERE username = ? AND used_at = 0`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SpendRecoveryCode marks the code with that stored hash used; false if it
// was already used (two servers racing, or a replay).
func (e *Engine) SpendRecoveryCode(name, hash string, now int64) (bool, error) {
	unlock := e.writeLock()
	defer unlock()
	res, err := e.db.Exec(`UPDATE local_recovery_codes SET used_at = ? WHERE username = ? AND code_hash = ? AND used_at = 0`, now, name, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoveryCodesLeft counts the unused recovery codes of an account.
func (e *Engine) RecoveryCodesLeft(name string) (int, error) {
	var n int
	err := e.db.QueryRow(`SELECT count(*) FROM local_recovery_codes WHERE username = ? AND used_at = 0`, name).Scan(&n)
	return n, err
}

// ProviderSignInSeen says whether anyone who is not a built-in account has
// signed in (through an identity provider), which is what "the provider
// works" means before the last built-in administrator may be removed.
func (e *Engine) ProviderSignInSeen() (bool, error) {
	var n int
	err := e.db.QueryRow(`SELECT count(*) FROM users WHERE username NOT IN (SELECT username FROM local_accounts)`).Scan(&n)
	return n > 0, err
}

// UnlockLocal clears the failure count and the lock of an account.
func (e *Engine) UnlockLocal(name string) error {
	return e.updateAccount(name, `failed_count = 0, locked_until = 0`)
}
