package engine

import (
	"github.com/jk-magic-creations/launchway/internal/store"
)

// One-time links and the one-time setup code of a new installation.

// Kinds of one-time link.
const (
	LinkInvite = "invite" // welcomes a new person, who chooses a password
	LinkReset  = "reset"  // lets a person who is locked out choose a new password and second factor
)

// Link is a one-time link, as stored (the secret part is kept only as a hash).
type Link = store.Link

// CreateLink stores a link and withdraws any earlier unused link of the same
// person and kind, so only the newest works.
func (e *Engine) CreateLink(l Link) error {
	return e.ident().CreateLink(e.scope0(), l)
}

// LinkByHash returns a link that is unused and not expired, or ErrNotFound
// (the same answer for unknown, used and expired, so a probe learns nothing).
func (e *Engine) LinkByHash(hash string, now int64) (*Link, error) {
	return e.ident().LinkByHash(e.scope0(), hash, now)
}

// UseLink marks a link used; false if it was already used or has expired.
func (e *Engine) UseLink(hash string, now int64) (bool, error) {
	return e.ident().UseLink(e.scope0(), hash, now)
}

// SetupState is the one-time code that starts an installation.
type SetupState = store.SetupState

// SetupCodeState returns the current setup code's state, or nil if none was ever issued.
func (e *Engine) SetupCodeState() (*SetupState, error) {
	return e.ident().SetupCodeState()
}

// IssueSetupCode records a setup code (as a hash), replacing an earlier one
// that has not been used. It refuses once setup has been completed.
func (e *Engine) IssueSetupCode(hash string, now, expires int64, source string) error {
	return e.ident().IssueSetupCode(hash, now, expires, source)
}

// ErrSetupCode is returned when the setup code is wrong, used or expired. One
// error for all three, so a guess learns nothing.
var ErrSetupCode = store.ErrSetupCode

// CompleteSetup creates the first administrator in one transaction with
// spending the setup code: if the code does not match the unused, unexpired
// one, or an account already exists, nothing is created.
func (e *Engine) CompleteSetup(codeHash string, a *LocalAccount, recoveryHashes []string, now int64) error {
	sealed, err := e.sealTOTP(a.Username, a.TOTPSecret)
	if err != nil {
		return err
	}
	return e.ident().CompleteSetup(e.scope0(), codeHash, a, sealed, recoveryHashes, now)
}
