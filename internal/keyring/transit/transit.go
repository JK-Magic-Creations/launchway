// Package transit is the root key holder that keeps the root key in
// HashiCorp Vault or OpenBao: the key-encryption keys are wrapped and
// unwrapped through the transit secrets engine, whose key is never
// exported. It reuses the server's vault client and login methods
// (internal/secrets).
package transit

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"

	"github.com/jk-magic-creations/launchway/internal/keyring"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// Config is where the transit key is, in the vault store's settings plus the
// mount and key names.
type Config struct {
	secrets.StoreConfig `yaml:",inline"`
	TransitMount        string `yaml:"transit_mount"` // default transit
	TransitKey          string `yaml:"transit_key"`
}

// Holder wraps through the service.
type Holder struct{ t *secrets.Transit }

// Open checks the settings (it does not contact the service) and returns the holder.
func Open(c Config) (*Holder, error) {
	t, err := secrets.OpenTransit(c.StoreConfig, c.TransitMount, c.TransitKey)
	if err != nil {
		return nil, err
	}
	return &Holder{t}, nil
}

var _ keyring.Holder = (*Holder)(nil)

func (h *Holder) Kind() string { return "transit" }

// Wrap sends a digest of the authenticated data with the key, so that a
// wrapped key only unwraps for the row it was made for (the service's key
// is not required to be a derived key).
func (h *Holder) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	d := sha256.Sum256(aad)
	ct, err := h.t.Encrypt(ctx, append(d[:], key...))
	if err != nil {
		return nil, err
	}
	return []byte(ct), nil
}

func (h *Holder) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	pt, err := h.t.Decrypt(ctx, string(wrapped))
	if err != nil {
		return nil, err
	}
	if len(pt) != 32+32 {
		return nil, errors.New("the transit service returned a value of the wrong size")
	}
	d := sha256.Sum256(aad)
	if subtle.ConstantTimeCompare(pt[:32], d[:]) != 1 {
		return nil, errors.New("a wrapped key was made for another place")
	}
	return pt[32:], nil
}

func (h *Holder) Status(ctx context.Context) keyring.HolderStatus {
	st := keyring.HolderStatus{Kind: "transit", Where: h.t.Where(), Reachable: true,
		Strength: "The root key never leaves the Vault or OpenBao transit service; every unwrap is logged there, and revoking the server's access makes every secret unreadable at once."}
	if _, err := h.t.Encrypt(ctx, []byte("launchway status check")); err != nil {
		st.Reachable, st.Problem = false, err.Error()
	}
	return st
}
