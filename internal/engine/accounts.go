package engine

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// Built-in accounts (schema 36): what the database keeps about people who
// sign in with a password held by this installation. The rules (the password
// policy, the throttle, who may do what) are the API's and internal/localauth's;
// this only stores, and keeps each multi-step change in one transaction.

// LocalAccount is one built-in account.
type LocalAccount = store.LocalAccount

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

// sealer is the box the store seals second factors with (nil if none was given).
func (e *Engine) sealer() store.Sealer {
	if b := e.totpBoxNow(); b != nil {
		return b
	}
	return nil
}

// noRootKey says in the engine's words that a second factor needs the root key.
func noRootKey(err error) error {
	if errors.Is(err, store.ErrSealUnavailable) {
		return fmt.Errorf("a second factor stores a secret: %w", keyring.ErrNoRootKey)
	}
	return err
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

// CreateLocalAccount adds an account; ErrConflict if the name is taken.
func (e *Engine) CreateLocalAccount(a *LocalAccount) error {
	return noRootKey(e.ident().CreateLocalAccount(e.scope0(), a, e.sealer()))
}

// LocalAccountByName returns the account, or ErrNotFound.
func (e *Engine) LocalAccountByName(name string) (*LocalAccount, error) {
	a, err := e.ident().LocalAccountByName(e.scope0(), name)
	if err != nil {
		return nil, err
	}
	return a, e.openTOTP(a)
}

// ListLocalAccounts returns every account by name.
func (e *Engine) ListLocalAccounts() ([]*LocalAccount, error) {
	out, err := e.ident().ListLocalAccounts(e.scope0())
	if err != nil {
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
	return e.ident().CountLocalAdmins(e.scope0())
}

// CountLocalAccounts counts every account.
func (e *Engine) CountLocalAccounts() (int, error) {
	return e.ident().CountLocalAccounts(e.scope0())
}

// SetLocalPassword stores a new password hash, clears the failure count and
// the lock, and ends every session of the account except keep (a session hash;
// "" ends them all), so a changed password signs out whoever else had it.
func (e *Engine) SetLocalPassword(name, hash string, now int64, keep string) error {
	return e.ident().SetPassword(e.scope0(), name, hash, now, keep)
}

// SetLocalRole changes the role of an account.
func (e *Engine) SetLocalRole(name, role string) error {
	return e.ident().SetRole(e.scope0(), name, role)
}

// SetLocalDisabled turns an account off or on; turning off ends its sessions.
func (e *Engine) SetLocalDisabled(name string, disabled bool) error {
	return e.ident().SetDisabled(e.scope0(), name, disabled)
}

// SetLocalBreakGlass marks or unmarks an account as the break-glass administrator.
func (e *Engine) SetLocalBreakGlass(name string, on bool) error {
	return e.ident().SetBreakGlass(e.scope0(), name, on)
}

// SetLocalProfile changes the display name and e-mail address.
func (e *Engine) SetLocalProfile(name, display, email string) error {
	return e.ident().SetProfile(e.scope0(), name, display, email)
}

// SetLocalTOTP stores a TOTP secret (unconfirmed until the person proves it
// with a code) or, with an empty secret, removes the second factor and the
// recovery codes with it.
func (e *Engine) SetLocalTOTP(name, secret string, confirmed bool) error {
	return noRootKey(e.ident().SetTOTP(e.scope0(), name, secret, confirmed, e.sealer()))
}

// RecordTOTPStep notes the step of a code just accepted so it cannot be used
// again; false if a code of that step or later was already used (a replay, or
// two servers racing).
func (e *Engine) RecordTOTPStep(name string, step int64) (bool, error) {
	return e.ident().RecordTOTPStep(e.scope0(), name, step)
}

// LocalFailure counts a failed sign-in and, when lockFor says so for the new
// count, locks the account until then. It returns the count and the lock's end.
func (e *Engine) LocalFailure(name string, now time.Time, lockFor func(failures int) time.Duration) (int64, int64, error) {
	return e.ident().Failure(e.scope0(), name, now, lockFor)
}

// LocalSuccess clears the failures and notes the sign-in.
func (e *Engine) LocalSuccess(name string, now int64) error {
	return e.ident().Success(e.scope0(), name, now)
}

// DeleteLocalAccount removes an account, its recovery codes, its links and its sessions.
func (e *Engine) DeleteLocalAccount(name string) error {
	return e.ident().DeleteLocalAccount(e.scope0(), name)
}

// EndUserSessions signs a person out everywhere, and says how many sessions ended.
func (e *Engine) EndUserSessions(name string) (int64, error) {
	return e.ident().EndUserSessions(e.scope0(), name)
}

// ReplaceRecoveryCodes stores the hashes of a new set of recovery codes and
// forgets the old set. nil removes them.
func (e *Engine) ReplaceRecoveryCodes(name string, hashes []string) error {
	return e.ident().ReplaceRecoveryCodes(e.scope0(), name, hashes)
}

// RecoveryCodeHashes returns the stored hashes of the unused recovery codes
// of an account, for the caller to compare a typed code with.
func (e *Engine) RecoveryCodeHashes(name string) ([]string, error) {
	return e.ident().RecoveryCodeHashes(e.scope0(), name)
}

// SpendRecoveryCode marks the code with that stored hash used; false if it
// was already used (two servers racing, or a replay).
func (e *Engine) SpendRecoveryCode(name, hash string, now int64) (bool, error) {
	return e.ident().SpendRecoveryCode(e.scope0(), name, hash, now)
}

// RecoveryCodesLeft counts the unused recovery codes of an account.
func (e *Engine) RecoveryCodesLeft(name string) (int, error) {
	return e.ident().RecoveryCodesLeft(e.scope0(), name)
}

// ProviderSignInSeen says whether anyone who is not a built-in account has
// signed in (through an identity provider), which is what "the provider
// works" means before the last built-in administrator may be removed.
func (e *Engine) ProviderSignInSeen() (bool, error) {
	return e.ident().ProviderSignInSeen(e.scope0())
}

// UnlockLocal clears the failure count and the lock of an account.
func (e *Engine) UnlockLocal(name string) error {
	return e.ident().Unlock(e.scope0(), name)
}
