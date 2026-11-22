package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Identity stores the people of a Base: who they are (users, built-in
// accounts, memberships), how they are signed in (sessions) and what they
// hold (API tokens, one-time links, recovery codes). Every method takes the
// Base's Scope, and every statement says base_id.
//
// A thing that exists in another Base is reported exactly like a thing that
// does not exist: ErrNotFound with the same words, because the statement never
// sees the other Base's rows (and on PostgreSQL could not).
type Identity struct {
	env *Env
}

// NewIdentity returns the identity store of an environment.
func NewIdentity(env *Env) *Identity { return &Identity{env: env} }

func sealTOTP(b Sealer, name, secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	if b == nil || !b.Enabled() {
		return "", ErrSealUnavailable
	}
	return b.SealRow(name, []byte(secret))
}

func newID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func notFound(what string) error { return fmt.Errorf("%w: %s", ErrNotFound, what) }

// ---- people, sessions ----

// SignIn records a person's groups and profile as their provider gave them
// now, their place in the Base, and a new session for them. Sessions that
// ended before staleBefore (idle) or now (absolute) are forgotten in passing.
func (i *Identity) SignIn(sc Scope, user string, groups []string, profile Profile, sessionHash string, now, expiresAt, staleBefore int64) error {
	g, err := json.Marshal(groups)
	if err != nil {
		return err
	}
	return i.env.Do(sc, true, func(q *Q) error {
		if _, err := q.Exec(`INSERT INTO users(base_id, username, group_list, last_login, display_name, email) VALUES(?,?,?,?,?,?)
			ON CONFLICT(base_id, username) DO UPDATE SET group_list = excluded.group_list, last_login = excluded.last_login,
				display_name = excluded.display_name, email = excluded.email`,
			q.Base(), user, string(g), now, profile.Name, profile.Email); err != nil {
			return err
		}
		if err := ensureMember(q, user, "", "active", now); err != nil {
			return err
		}
		if _, err := q.Exec(`INSERT INTO sessions(base_id, id_hash, username, created_at, last_seen, expires_at) VALUES(?,?,?,?,?,?)`,
			q.Base(), sessionHash, user, now, now, expiresAt); err != nil {
			return err
		}
		_, err := q.Exec(`DELETE FROM sessions WHERE base_id = ? AND (expires_at <= ? OR last_seen < ?)`, q.Base(), now, staleBefore)
		return err
	})
}

// SessionByHash looks a session up in the Base. ErrNotFound if there is none.
func (i *Identity) SessionByHash(sc Scope, hash string) (*Session, error) {
	s := &Session{}
	var groups, name, email sql.NullString
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT s.username, u.group_list, u.display_name, u.email, s.created_at, s.last_seen, s.expires_at
			FROM sessions s LEFT JOIN users u ON u.base_id = s.base_id AND u.username = s.username
			WHERE s.base_id = ? AND s.id_hash = ?`, q.Base(), hash).
			Scan(&s.User, &groups, &name, &email, &s.CreatedAt, &s.LastSeen, &s.ExpiresAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("session")
	}
	if err != nil {
		return nil, err
	}
	s.Groups = decodeGroups(groups)
	s.Profile = Profile{Name: name.String, Email: email.String}
	return s, nil
}

// TouchSession records that a session was used at now, unless that was
// already recorded since notBefore.
func (i *Identity) TouchSession(sc Scope, hash string, now, notBefore int64) error {
	return i.env.Do(sc, true, func(q *Q) error {
		_, err := q.Exec(`UPDATE sessions SET last_seen = ? WHERE base_id = ? AND id_hash = ? AND last_seen < ?`, now, q.Base(), hash, notBefore)
		return err
	})
}

// EndSession deletes a session and returns whose it was ("" if there was none).
func (i *Identity) EndSession(sc Scope, hash string) (string, error) {
	var user string
	err := i.env.Do(sc, true, func(q *Q) error {
		return q.QueryRow(`DELETE FROM sessions WHERE base_id = ? AND id_hash = ? RETURNING username`, q.Base(), hash).Scan(&user)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return user, err
}

// EndSessionsExcept ends every session of a person except keep ("" ends all).
func (i *Identity) EndSessionsExcept(sc Scope, name, keep string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		_, err := q.Exec(`DELETE FROM sessions WHERE base_id = ? AND username = ? AND id_hash <> ?`, q.Base(), name, keep)
		return err
	})
}

// EndUserSessions signs a person out everywhere, and says how many sessions ended.
func (i *Identity) EndUserSessions(sc Scope, name string) (int64, error) {
	var n int64
	err := i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`DELETE FROM sessions WHERE base_id = ? AND username = ?`, q.Base(), name)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// CountSessions counts a person's sessions.
func (i *Identity) CountSessions(sc Scope, name string) (int, error) {
	var n int
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT count(*) FROM sessions WHERE base_id = ? AND username = ?`, q.Base(), name).Scan(&n)
	})
	return n, err
}

// LastSignIn returns a person's groups and profile as of their latest
// sign-in; ErrNotFound if they never signed in to this Base.
func (i *Identity) LastSignIn(sc Scope, user string) ([]string, Profile, error) {
	var groups, name, email sql.NullString
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT group_list, display_name, email FROM users WHERE base_id = ? AND username = ?`, q.Base(), user).Scan(&groups, &name, &email)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Profile{}, notFound("user " + user)
	}
	if err != nil {
		return nil, Profile{}, err
	}
	return decodeGroups(groups), Profile{Name: name.String, Email: email.String}, nil
}

// ProviderSignInSeen says whether anyone who is not a built-in account has
// signed in (through an identity provider) to this Base.
func (i *Identity) ProviderSignInSeen(sc Scope) (bool, error) {
	var n int
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT count(*) FROM users WHERE base_id = ? AND username NOT IN (SELECT username FROM local_accounts WHERE base_id = ?)`,
			q.Base(), q.Base()).Scan(&n)
	})
	return n > 0, err
}

// ---- tokens ----

// CreateToken stores a token under the stored form of the hash of its value
// (the caller adds any prefix that marks a limited token). A person's
// tokens have different names.
func (i *Identity) CreateToken(sc Scope, t APIToken, storedHash string) error {
	projects, _ := json.Marshal(t.Projects)
	if len(t.Projects) == 0 {
		projects = []byte("")
	}
	repos, ranges := encodeList(t.Repositories), encodeList(t.SourceRanges)
	err := i.env.Do(sc, true, func(q *Q) error {
		_, err := q.Exec(`INSERT INTO api_tokens(base_id, id, hash, username, name, created_at, expires_at, role, projects, repos, repo_access, source_ranges) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			q.Base(), t.ID, storedHash, t.User, t.Name, t.CreatedAt, t.ExpiresAt, t.Role, string(projects), repos, t.RepoAccess, ranges)
		return err
	})
	if IsConflict(err) {
		return fmt.Errorf("%w: you already have a token called %q", ErrConflict, t.Name)
	}
	return err
}

const tokenColumns = `t.id, t.name, t.username, t.created_at, t.expires_at, t.last_used, t.role, t.projects, t.last_used_from, t.repos, t.repo_access, t.source_ranges` //nolint:gosec // column names, not a credential

func scanToken(sc Row, extra ...any) (*APIToken, error) {
	t := &APIToken{}
	var projects, repos, ranges string
	if err := sc.Scan(append([]any{&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed, &t.Role, &projects, &t.LastUsedFrom, &repos, &t.RepoAccess, &ranges}, extra...)...); err != nil {
		return nil, err
	}
	if repos != "" {
		_ = json.Unmarshal([]byte(repos), &t.Repositories)
	}
	if ranges != "" {
		_ = json.Unmarshal([]byte(ranges), &t.SourceRanges)
	}
	if projects != "" {
		_ = json.Unmarshal([]byte(projects), &t.Projects)
	}
	return t, nil
}

// TokenByHash finds a token of this Base by either stored form of the hash of
// its value, with its owner's groups as of their latest sign-in. A token of
// another Base is not found.
func (i *Identity) TokenByHash(sc Scope, hash string, limitedHashes ...string) (*APIToken, []string, error) {
	var groups, name, email sql.NullString
	var t *APIToken
	err := i.env.Do(sc, false, func(q *Q) error {
		var err error
		t, err = scanToken(q.QueryRow(`SELECT `+tokenColumns+`, u.group_list, u.display_name, u.email
			FROM api_tokens t LEFT JOIN users u ON u.base_id = t.base_id AND u.username = t.username
			WHERE t.base_id = ? AND t.hash IN (`+strings.TrimSuffix(strings.Repeat("?,", 1+len(limitedHashes)), ",")+`)`,
			append([]any{q.Base(), hash}, anys(limitedHashes)...)...), &groups, &name, &email)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, notFound("token")
	}
	if err != nil {
		return nil, nil, err
	}
	t.Owner = Profile{Name: name.String, Email: email.String}
	return t, decodeGroups(groups), nil
}

// TouchToken records that a token was used at now from an address, unless
// that was already recorded since notBefore from the same address.
func (i *Identity) TouchToken(sc Scope, id string, now, notBefore int64, addr string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		_, err := q.Exec(`UPDATE api_tokens SET last_used = ?, last_used_from = ? WHERE base_id = ? AND id = ? AND (last_used < ? OR last_used_from <> ?)`,
			now, addr, q.Base(), id, notBefore, addr)
		return err
	})
}

// Tokens lists a person's tokens, or with user "" everyone's in the Base,
// newest first.
func (i *Identity) Tokens(sc Scope, user string) ([]APIToken, error) {
	out := []APIToken{}
	err := i.env.Do(sc, false, func(q *Q) error {
		query, args := `SELECT `+tokenColumns+` FROM api_tokens t WHERE t.base_id = ?`, []any{q.Base()}
		if user != "" {
			query += ` AND t.username = ?`
			args = append(args, user)
		}
		rows, err := q.Query(query+` ORDER BY t.created_at DESC, t.id DESC`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanToken(rows)
			if err != nil {
				return err
			}
			if user == "" {
				t.OwnerName = t.User
			}
			out = append(out, *t)
		}
		return rows.Err()
	})
	return out, err
}

// RevokeToken deletes one of a person's tokens. Someone else's token, and a
// token of another Base, are reported as not found.
func (i *Identity) RevokeToken(sc Scope, user, id string) (*APIToken, error) {
	t := &APIToken{}
	err := i.env.Do(sc, true, func(q *Q) error {
		return q.QueryRow(`DELETE FROM api_tokens WHERE base_id = ? AND id = ? AND username = ?
			RETURNING id, name, username, created_at, expires_at, last_used`, q.Base(), id, user).
			Scan(&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("token " + id)
	}
	return t, err
}

// RevokeAnyToken deletes anyone's token in the Base, for an administrator of it.
func (i *Identity) RevokeAnyToken(sc Scope, id string) (*APIToken, error) {
	t := &APIToken{}
	err := i.env.Do(sc, true, func(q *Q) error {
		return q.QueryRow(`DELETE FROM api_tokens WHERE base_id = ? AND id = ? RETURNING id, name, username, created_at, expires_at, last_used`, q.Base(), id).
			Scan(&t.ID, &t.Name, &t.User, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("token " + id)
	}
	return t, err
}

// ---- built-in accounts ----

// localAccountColumns lists the columns of local_accounts in scan order.
const localAccountColumns = `username, display_name, email, role, password_hash, password_set_at, totp_secret, totp_confirmed,
	totp_last_step, failed_count, locked_until, disabled, break_glass, created_at, created_by, last_login_at`

func scanLocalAccount(row Row) (*LocalAccount, error) {
	a := &LocalAccount{}
	var confirmed, disabled, bg int
	err := row.Scan(&a.Username, &a.DisplayName, &a.Email, &a.Role, &a.PasswordHash, &a.PasswordSetAt, &a.TOTPSecret, &confirmed,
		&a.TOTPLastStep, &a.FailedCount, &a.LockedUntil, &disabled, &bg, &a.CreatedAt, &a.CreatedBy, &a.LastLoginAt)
	a.TOTPConfirmed, a.Disabled, a.BreakGlass = confirmed != 0, disabled != 0, bg != 0
	return a, err
}

func insertLocalAccount(q *Q, a *LocalAccount, sealed string) error {
	if _, err := q.Exec(`INSERT INTO local_accounts(base_id, username, display_name, email, role, password_hash, password_set_at, totp_secret, totp_confirmed,
	totp_last_step, failed_count, locked_until, disabled, break_glass, created_at, created_by, last_login_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		q.Base(), a.Username, a.DisplayName, a.Email, a.Role, a.PasswordHash, a.PasswordSetAt, sealed, b2i(a.TOTPConfirmed),
		a.TOTPLastStep, a.FailedCount, a.LockedUntil, b2i(a.Disabled), b2i(a.BreakGlass), a.CreatedAt, a.CreatedBy, a.LastLoginAt); err != nil {
		return err
	}
	state := "active"
	if a.Disabled {
		state = "disabled"
	}
	return ensureMember(q, a.Username, a.Role, state, a.CreatedAt)
}

// CreateLocalAccount adds an account, sealing its second factor with box;
// ErrConflict if the name is taken in this Base. The same name in another
// Base is not a clash and is not noticed.
func (i *Identity) CreateLocalAccount(sc Scope, a *LocalAccount, box Sealer) error {
	sealed, err := sealTOTP(box, a.Username, a.TOTPSecret)
	if err != nil {
		return err
	}
	return i.env.Do(sc, true, func(q *Q) error {
		var n int
		if err := q.QueryRow(`SELECT count(*) FROM local_accounts WHERE base_id = ? AND username = ?`, q.Base(), a.Username).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: there is already an account called %s", ErrConflict, a.Username)
		}
		return insertLocalAccount(q, a, sealed)
	})
}

// LocalAccountByName returns the account (second factor still sealed), or ErrNotFound.
func (i *Identity) LocalAccountByName(sc Scope, name string) (*LocalAccount, error) {
	var a *LocalAccount
	err := i.env.Do(sc, false, func(q *Q) error {
		var err error
		a, err = scanLocalAccount(q.QueryRow(`SELECT `+localAccountColumns+` FROM local_accounts WHERE base_id = ? AND username = ?`, q.Base(), name))
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("account " + name)
	}
	return a, err
}

// ListLocalAccounts returns every account of the Base by name.
func (i *Identity) ListLocalAccounts(sc Scope) ([]*LocalAccount, error) {
	var out []*LocalAccount
	err := i.env.Do(sc, false, func(q *Q) error {
		rows, err := q.Query(`SELECT `+localAccountColumns+` FROM local_accounts WHERE base_id = ? ORDER BY username`, q.Base())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanLocalAccount(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// CountLocalAdmins counts the enabled administrators among the Base's accounts.
func (i *Identity) CountLocalAdmins(sc Scope) (int, error) {
	var n int
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT count(*) FROM local_accounts WHERE base_id = ? AND role = 'admin' AND disabled = 0`, q.Base()).Scan(&n)
	})
	return n, err
}

// CountLocalAccounts counts the Base's accounts.
func (i *Identity) CountLocalAccounts(sc Scope) (int, error) {
	var n int
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT count(*) FROM local_accounts WHERE base_id = ?`, q.Base()).Scan(&n)
	})
	return n, err
}

func affected(res sql.Result, what string) error {
	if n, _ := res.RowsAffected(); n == 0 {
		return notFound("account " + what)
	}
	return nil
}

// SetPassword stores a new password hash, clears the failure count and the
// lock, and ends every session of the account except keep.
func (i *Identity) SetPassword(sc Scope, name, hash string, now int64, keep string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET password_hash = ?, password_set_at = ?, failed_count = 0, locked_until = 0 WHERE base_id = ? AND username = ?`,
			hash, now, q.Base(), name)
		if err != nil {
			return err
		}
		if err := affected(res, name); err != nil {
			return err
		}
		_, err = q.Exec(`DELETE FROM sessions WHERE base_id = ? AND username = ? AND id_hash <> ?`, q.Base(), name, keep)
		return err
	})
}

// SetRole changes the role of an account, in the account and in the
// membership.
func (i *Identity) SetRole(sc Scope, name, role string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET role = ? WHERE base_id = ? AND username = ?`, role, q.Base(), name)
		if err != nil {
			return err
		}
		if err := affected(res, name); err != nil {
			return err
		}
		_, err = q.Exec(`UPDATE memberships SET role = ? WHERE base_id = ? AND handle = ?`, role, q.Base(), name)
		return err
	})
}

// SetDisabled turns an account off or on; turning off ends its sessions.
func (i *Identity) SetDisabled(sc Scope, name string, disabled bool) error {
	state := "active"
	if disabled {
		state = "disabled"
	}
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET disabled = ? WHERE base_id = ? AND username = ?`, b2i(disabled), q.Base(), name)
		if err != nil {
			return err
		}
		if err := affected(res, name); err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE memberships SET state = ? WHERE base_id = ? AND handle = ?`, state, q.Base(), name); err != nil {
			return err
		}
		if disabled {
			_, err = q.Exec(`DELETE FROM sessions WHERE base_id = ? AND username = ?`, q.Base(), name)
		}
		return err
	})
}

// SetBreakGlass marks or unmarks an account as the break-glass administrator.
func (i *Identity) SetBreakGlass(sc Scope, name string, on bool) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET break_glass = ? WHERE base_id = ? AND username = ?`, b2i(on), q.Base(), name)
		if err != nil {
			return err
		}
		return affected(res, name)
	})
}

// SetProfile changes the display name and e-mail address.
func (i *Identity) SetProfile(sc Scope, name, display, email string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET display_name = ?, email = ? WHERE base_id = ? AND username = ?`, display, email, q.Base(), name)
		if err != nil {
			return err
		}
		return affected(res, name)
	})
}

// SetTOTP seals and stores a TOTP secret, unconfirmed until the person proves
// it, or, with an empty one, removes the second factor and the recovery codes
// with it.
func (i *Identity) SetTOTP(sc Scope, name, secret string, confirmed bool, box Sealer) error {
	sealed, err := sealTOTP(box, name, secret)
	if err != nil {
		return err
	}
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET totp_secret = ?, totp_confirmed = ?, totp_last_step = 0 WHERE base_id = ? AND username = ?`,
			sealed, b2i(confirmed), q.Base(), name)
		if err != nil {
			return err
		}
		if err := affected(res, name); err != nil {
			return err
		}
		if sealed == "" {
			_, err = q.Exec(`DELETE FROM local_recovery_codes WHERE base_id = ? AND username = ?`, q.Base(), name)
		}
		return err
	})
}

// RecordTOTPStep notes the step of a code just accepted so it cannot be used
// again; false if a code of that step or later was already used.
func (i *Identity) RecordTOTPStep(sc Scope, name string, step int64) (bool, error) {
	var ok bool
	err := i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET totp_last_step = ? WHERE base_id = ? AND username = ? AND totp_last_step < ?`, step, q.Base(), name, step)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ok = n == 1
		return nil
	})
	return ok, err
}

// Failure counts a failed sign-in and, when lockFor says so for the new
// count, locks the account until then. It returns the count and the lock's end.
func (i *Identity) Failure(sc Scope, name string, now time.Time, lockFor func(failures int) time.Duration) (int64, int64, error) {
	var n, until int64
	err := i.env.Do(sc, true, func(q *Q) error {
		err := q.QueryRow(`UPDATE local_accounts SET failed_count = failed_count + 1 WHERE base_id = ? AND username = ? RETURNING failed_count`, q.Base(), name).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			n = 0
			return nil
		}
		if err != nil {
			return err
		}
		if d := lockFor(int(n)); d > 0 {
			until = now.Add(d).UnixMilli()
			_, err = q.Exec(`UPDATE local_accounts SET locked_until = ? WHERE base_id = ? AND username = ?`, until, q.Base(), name)
		}
		return err
	})
	return n, until, err
}

// Success clears the failures and notes the sign-in.
func (i *Identity) Success(sc Scope, name string, now int64) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET failed_count = 0, locked_until = 0, last_login_at = ? WHERE base_id = ? AND username = ?`, now, q.Base(), name)
		if err != nil {
			return err
		}
		return affected(res, name)
	})
}

// Unlock clears the failure count and the lock of an account.
func (i *Identity) Unlock(sc Scope, name string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_accounts SET failed_count = 0, locked_until = 0 WHERE base_id = ? AND username = ?`, q.Base(), name)
		if err != nil {
			return err
		}
		return affected(res, name)
	})
}

// DeleteLocalAccount removes an account, its recovery codes, its links, its
// sessions and its place in the Base.
func (i *Identity) DeleteLocalAccount(sc Scope, name string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`DELETE FROM local_accounts WHERE base_id = ? AND username = ?`, q.Base(), name)
		if err != nil {
			return err
		}
		if err := affected(res, name); err != nil {
			return err
		}
		for _, stmt := range []string{
			`DELETE FROM local_recovery_codes WHERE base_id = ? AND username = ?`,
			`DELETE FROM local_links WHERE base_id = ? AND username = ?`,
			`DELETE FROM sessions WHERE base_id = ? AND username = ?`,
		} {
			if _, err := q.Exec(stmt, q.Base(), name); err != nil {
				return err
			}
		}
		return removeMember(q, name)
	})
}

// ReplaceRecoveryCodes stores the hashes of a new set of recovery codes and
// forgets the old set. nil removes them.
func (i *Identity) ReplaceRecoveryCodes(sc Scope, name string, hashes []string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		if _, err := q.Exec(`DELETE FROM local_recovery_codes WHERE base_id = ? AND username = ?`, q.Base(), name); err != nil {
			return err
		}
		for _, h := range hashes {
			if _, err := q.Exec(`INSERT INTO local_recovery_codes(base_id, username, code_hash) VALUES(?,?,?)`, q.Base(), name, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecoveryCodeHashes returns the stored hashes of the unused recovery codes.
func (i *Identity) RecoveryCodeHashes(sc Scope, name string) ([]string, error) {
	var out []string
	err := i.env.Do(sc, false, func(q *Q) error {
		rows, err := q.Query(`SELECT code_hash FROM local_recovery_codes WHERE base_id = ? AND username = ? AND used_at = 0`, q.Base(), name)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// SpendRecoveryCode marks the code with that stored hash used; false if it
// was already used (two servers racing, or a replay).
func (i *Identity) SpendRecoveryCode(sc Scope, name, hash string, now int64) (bool, error) {
	var ok bool
	err := i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_recovery_codes SET used_at = ? WHERE base_id = ? AND username = ? AND code_hash = ? AND used_at = 0`, now, q.Base(), name, hash)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ok = n == 1
		return nil
	})
	return ok, err
}

// RecoveryCodesLeft counts the unused recovery codes of an account.
func (i *Identity) RecoveryCodesLeft(sc Scope, name string) (int, error) {
	var n int
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT count(*) FROM local_recovery_codes WHERE base_id = ? AND username = ? AND used_at = 0`, q.Base(), name).Scan(&n)
	})
	return n, err
}

// ---- one-time links ----

// CreateLink stores a link and withdraws any earlier unused link of the same
// person and kind, so only the newest works.
func (i *Identity) CreateLink(sc Scope, l Link) error {
	return i.env.Do(sc, true, func(q *Q) error {
		if _, err := q.Exec(`DELETE FROM local_links WHERE base_id = ? AND username = ? AND kind = ? AND used_at = 0`, q.Base(), l.Username, l.Kind); err != nil {
			return err
		}
		_, err := q.Exec(`INSERT INTO local_links(base_id, token_hash, username, kind, role, created_at, created_by, expires_at) VALUES(?,?,?,?,?,?,?,?)`,
			q.Base(), l.Hash, l.Username, l.Kind, l.Role, l.CreatedAt, l.CreatedBy, l.ExpiresAt)
		return err
	})
}

// LinkByHash returns a link that is unused and not expired, or ErrNotFound
// (the same answer for unknown, used, expired and another Base's).
func (i *Identity) LinkByHash(sc Scope, hash string, now int64) (*Link, error) {
	l := &Link{}
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT token_hash, username, kind, role, created_at, created_by, expires_at, used_at FROM local_links
			WHERE base_id = ? AND token_hash = ? AND used_at = 0 AND expires_at > ?`, q.Base(), hash, now).
			Scan(&l.Hash, &l.Username, &l.Kind, &l.Role, &l.CreatedAt, &l.CreatedBy, &l.ExpiresAt, &l.UsedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("link")
	}
	return l, err
}

// UseLink marks a link used; false if it was already used or has expired.
func (i *Identity) UseLink(sc Scope, hash string, now int64) (bool, error) {
	var ok bool
	err := i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE local_links SET used_at = ? WHERE base_id = ? AND token_hash = ? AND used_at = 0 AND expires_at > ?`, now, q.Base(), hash, now)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ok = n == 1
		return nil
	})
	return ok, err
}

// ---- the installation's setup code (global) and the first administrator ----

// SetupCodeState returns the current setup code's state, or nil if none was ever issued.
func (i *Identity) SetupCodeState() (*SetupState, error) {
	s := &SetupState{}
	err := i.env.doGlobal(false, func(q Querier) error {
		return q.QueryRow(`SELECT code_hash, issued_at, expires_at, used_at, source FROM setup_state WHERE id = 1`).
			Scan(&s.CodeHash, &s.IssuedAt, &s.ExpiresAt, &s.UsedAt, &s.Source)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// IssueSetupCode records a setup code (as a hash), replacing an earlier one
// that has not been used. It refuses once setup has been completed.
func (i *Identity) IssueSetupCode(hash string, now, expires int64, source string) error {
	return i.env.doGlobal(true, func(q Querier) error {
		var used int64
		err := q.QueryRow(`SELECT used_at FROM setup_state WHERE id = 1`).Scan(&used)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// Servers of one installation that start together on a shared database all get here: the insert must
			// not fail for the one that loses the race (that server would exit and restart at every fresh
			// high-availability install). A row made meanwhile is replaced by this code while it is unused (the
			// servers hold the same code when it comes from the chart's Secret), and refused once setup was completed.
			res, err := q.Exec(`INSERT INTO setup_state(id, code_hash, issued_at, expires_at, source) VALUES(1,?,?,?,?)
				ON CONFLICT(id) DO UPDATE SET code_hash = excluded.code_hash, issued_at = excluded.issued_at, expires_at = excluded.expires_at, source = excluded.source
				WHERE setup_state.used_at = 0`, hash, now, expires, source)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return fmt.Errorf("%w: setup was already completed", ErrConflict)
			}
			return nil
		case err != nil:
			return err
		case used != 0:
			return fmt.Errorf("%w: setup was already completed", ErrConflict)
		}
		_, err = q.Exec(`UPDATE setup_state SET code_hash = ?, issued_at = ?, expires_at = ?, source = ? WHERE id = 1`, hash, now, expires, source)
		return err
	})
}

// ErrSetupCode is returned when the setup code is wrong, used or expired. One
// error for all three, so a guess learns nothing.
var ErrSetupCode = errors.New("the setup code is not valid")

// CompleteSetup creates the first administrator in the Base in one
// transaction with spending the setup code: if the code does not match the
// unused, unexpired one, or an account already exists, nothing is created.
func (i *Identity) CompleteSetup(sc Scope, codeHash string, a *LocalAccount, box Sealer, recoveryHashes []string, now int64) error {
	sealed, err := sealTOTP(box, a.Username, a.TOTPSecret)
	if err != nil {
		return err
	}
	return i.env.Do(sc, true, func(q *Q) error {
		var n int
		if err := q.QueryRow(`SELECT count(*) FROM local_accounts WHERE base_id = ?`, q.Base()).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: setup was already completed", ErrConflict)
		}
		res, err := q.Exec(`UPDATE setup_state SET used_at = ? WHERE id = 1 AND code_hash = ? AND used_at = 0 AND expires_at > ?`, now, codeHash, now)
		if err != nil {
			return err
		}
		if c, _ := res.RowsAffected(); c != 1 {
			return ErrSetupCode
		}
		if err := insertLocalAccount(q, a, sealed); err != nil {
			return err
		}
		for _, h := range recoveryHashes {
			if _, err := q.Exec(`INSERT INTO local_recovery_codes(base_id, username, code_hash) VALUES(?,?,?)`, q.Base(), a.Username, h); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- memberships ----

// ensureMember gives the handle a place in the Base if it has none (an
// account is made for it) and, when role is given, records the role and state.
func ensureMember(q *Q, handle, role, state string, now int64) error {
	var accountID string
	err := q.QueryRow(`SELECT account_id FROM memberships WHERE base_id = ? AND handle = ?`, q.Base(), handle).Scan(&accountID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return addMember(q, handle, role, state, now, "")
	case err != nil:
		return err
	}
	if role != "" {
		_, err = q.Exec(`UPDATE memberships SET role = ?, state = ? WHERE base_id = ? AND handle = ?`, role, state, q.Base(), handle)
	}
	return err
}

func addMember(q *Q, handle, role, state string, now int64, accountID string) error {
	if accountID == "" {
		accountID = newID("ac_")
		if _, err := q.Exec(`INSERT INTO accounts(id, created_at) VALUES(?,?)`, accountID, now); err != nil {
			return err
		}
	}
	_, err := q.Exec(`INSERT INTO memberships(base_id, account_id, handle, role, state, created_at) VALUES(?,?,?,?,?,?)`,
		q.Base(), accountID, handle, role, state, now)
	return err
}

// removeMember takes the handle out of the Base. The account id it leaves
// behind is opaque and holds no data; it is swept by the operator's job
// (Stage 8), which alone may look across Bases.
func removeMember(q *Q, handle string) error {
	_, err := q.Exec(`DELETE FROM memberships WHERE base_id = ? AND handle = ?`, q.Base(), handle)
	return err
}

// AddMember gives an account a handle and role in the Base. With accountID
// "" a new account is made; with an existing account's id (a person who
// belongs to two Bases) it joins that account. The handle is unique within
// the Base; the same handle in another Base is nobody's business.
func (i *Identity) AddMember(sc Scope, handle, role, accountID string, now int64) (string, error) {
	var id string
	err := i.env.Do(sc, true, func(q *Q) error {
		if accountID != "" {
			var n int
			if err := q.QueryRow(`SELECT count(*) FROM accounts WHERE id = ?`, accountID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return notFound("account")
			}
		}
		var taken int
		if err := q.QueryRow(`SELECT count(*) FROM memberships WHERE base_id = ? AND handle = ?`, q.Base(), handle).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("%w: there is already a member called %s", ErrConflict, handle)
		}
		if accountID == "" {
			accountID = newID("ac_")
			if _, err := q.Exec(`INSERT INTO accounts(id, created_at) VALUES(?,?)`, accountID, now); err != nil {
				return err
			}
		}
		id = accountID
		_, err := q.Exec(`INSERT INTO memberships(base_id, account_id, handle, role, state, created_at) VALUES(?,?,?,?,?,?)`,
			q.Base(), accountID, handle, role, "active", now)
		if IsConflict(err) {
			return fmt.Errorf("%w: that account is already a member", ErrConflict)
		}
		return err
	})
	return id, err
}

// Members lists the Base's members by handle.
func (i *Identity) Members(sc Scope) ([]Member, error) {
	var out []Member
	err := i.env.Do(sc, false, func(q *Q) error {
		rows, err := q.Query(`SELECT account_id, handle, role, state, created_at FROM memberships WHERE base_id = ? ORDER BY handle`, q.Base())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			if err := rows.Scan(&m.AccountID, &m.Handle, &m.Role, &m.State, &m.CreatedAt); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// MemberByHandle returns one member; ErrNotFound if the Base has none by that handle.
func (i *Identity) MemberByHandle(sc Scope, handle string) (*Member, error) {
	m := &Member{}
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT account_id, handle, role, state, created_at FROM memberships WHERE base_id = ? AND handle = ?`, q.Base(), handle).
			Scan(&m.AccountID, &m.Handle, &m.Role, &m.State, &m.CreatedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("member " + handle)
	}
	return m, err
}

// MemberByAccount returns an account's membership in the Base; ErrNotFound if
// it has none here.
func (i *Identity) MemberByAccount(sc Scope, accountID string) (*Member, error) {
	m := &Member{}
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT account_id, handle, role, state, created_at FROM memberships WHERE base_id = ? AND account_id = ?`, q.Base(), accountID).
			Scan(&m.AccountID, &m.Handle, &m.Role, &m.State, &m.CreatedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("member")
	}
	return m, err
}

// SetMemberRole changes a member's role.
func (i *Identity) SetMemberRole(sc Scope, handle, role string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`UPDATE memberships SET role = ? WHERE base_id = ? AND handle = ?`, role, q.Base(), handle)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("member " + handle)
		}
		return nil
	})
}

// RemoveMember removes a member from the Base.
func (i *Identity) RemoveMember(sc Scope, handle string) error {
	return i.env.Do(sc, true, func(q *Q) error { return removeMember(q, strings.TrimSpace(handle)) })
}

// encodeList is a list as JSON, or "" for none.
func encodeList[T any](l []T) string {
	if len(l) == 0 {
		return ""
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// RewriteTokenRepos lets fn change the repository list of every token of the
// Base that has one (a repository was renamed or deleted); fn returns the new
// list and whether it changed.
func (i *Identity) RewriteTokenRepos(sc Scope, fn func([]TokenRepo) ([]TokenRepo, bool)) error {
	return i.env.Do(sc, true, func(q *Q) error {
		rows, err := q.Query(`SELECT id, repos FROM api_tokens WHERE base_id = ? AND repos <> ''`, q.Base())
		if err != nil {
			return err
		}
		type upd struct{ id, repos string }
		var todo []upd
		for rows.Next() {
			var id, raw string
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var list []TokenRepo
			if json.Unmarshal([]byte(raw), &list) != nil {
				continue
			}
			if out, changed := fn(list); changed {
				todo = append(todo, upd{id, encodeList(out)})
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, u := range todo {
			if _, err := q.Exec(`UPDATE api_tokens SET repos = ? WHERE base_id = ? AND id = ?`, u.repos, q.Base(), u.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- service accounts ----

// CreateServiceAccount adds a service account; ErrConflict if the name is taken.
func (i *Identity) CreateServiceAccount(sc Scope, a ServiceAccount) error {
	err := i.env.Do(sc, true, func(q *Q) error {
		_, err := q.Exec(`INSERT INTO service_accounts(base_id, name, description, role, disabled, created_at, created_by) VALUES(?,?,?,?,0,?,?)`,
			q.Base(), a.Name, a.Description, a.Role, a.CreatedAt, a.CreatedBy)
		return err
	})
	if IsConflict(err) {
		return fmt.Errorf("%w: a service account called %q exists", ErrConflict, a.Name)
	}
	return err
}

// ServiceAccounts lists the service accounts, by name.
func (i *Identity) ServiceAccounts(sc Scope) ([]ServiceAccount, error) {
	out := []ServiceAccount{}
	err := i.env.Do(sc, false, func(q *Q) error {
		rows, err := q.Query(`SELECT name, description, role, disabled, created_at, created_by FROM service_accounts WHERE base_id = ? ORDER BY name`, q.Base())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a ServiceAccount
			var dis int
			if err := rows.Scan(&a.Name, &a.Description, &a.Role, &dis, &a.CreatedAt, &a.CreatedBy); err != nil {
				return err
			}
			a.Disabled = dis != 0
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// ServiceAccount finds one by name.
func (i *Identity) ServiceAccount(sc Scope, name string) (*ServiceAccount, error) {
	var a ServiceAccount
	var dis int
	err := i.env.Do(sc, false, func(q *Q) error {
		return q.QueryRow(`SELECT name, description, role, disabled, created_at, created_by FROM service_accounts WHERE base_id = ? AND name = ?`, q.Base(), name).
			Scan(&a.Name, &a.Description, &a.Role, &dis, &a.CreatedAt, &a.CreatedBy)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, notFound("service account " + name)
	}
	a.Disabled = dis != 0
	return &a, err
}

// SetServiceAccount changes a service account's role and whether it is off.
func (i *Identity) SetServiceAccount(sc Scope, name string, role *string, disabled *bool) error {
	return i.env.Do(sc, true, func(q *Q) error {
		var found int64
		if role != nil {
			res, err := q.Exec(`UPDATE service_accounts SET role = ? WHERE base_id = ? AND name = ?`, *role, q.Base(), name)
			if err != nil {
				return err
			}
			found, _ = res.RowsAffected()
		}
		if disabled != nil {
			d := 0
			if *disabled {
				d = 1
			}
			res, err := q.Exec(`UPDATE service_accounts SET disabled = ? WHERE base_id = ? AND name = ?`, d, q.Base(), name)
			if err != nil {
				return err
			}
			found, _ = res.RowsAffected()
		}
		if found == 0 {
			return notFound("service account " + name)
		}
		return nil
	})
}

// DeleteServiceAccount removes the account and every token it owns.
func (i *Identity) DeleteServiceAccount(sc Scope, name, principal string) error {
	return i.env.Do(sc, true, func(q *Q) error {
		res, err := q.Exec(`DELETE FROM service_accounts WHERE base_id = ? AND name = ?`, q.Base(), name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return notFound("service account " + name)
		}
		_, err = q.Exec(`DELETE FROM api_tokens WHERE base_id = ? AND username = ?`, q.Base(), principal)
		return err
	})
}

func anys(l []string) []any {
	out := make([]any, len(l))
	for i, v := range l {
		out[i] = v
	}
	return out
}
