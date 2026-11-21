package keyring

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
)

// Holder keeps the installation's root key outside the database. It does
// two things with 32-byte keys: wrap one under additional authenticated
// data, and unwrap it again. Whether the root key itself is in this
// process (key file, environment, passphrase) or stays in a service or
// device (Vault or OpenBao transit; a cloud KMS, PKCS#11 or TPM holder
// later) is the holder's business.
//
// Kind is stored with every wrapped key so a database wrapped by one kind
// of holder is never opened by guessing with another.
type Holder interface {
	// Kind names the kind of holder: "key_file", "env", "passphrase",
	// "transit".
	Kind() string
	// Wrap seals a 32-byte key under the root key, binding aad.
	Wrap(ctx context.Context, key, aad []byte) ([]byte, error)
	// Unwrap reverses Wrap. It fails when the root key is not the one that
	// wrapped, or aad differs.
	Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error)
	// Status describes the holder for people: never key material.
	Status(ctx context.Context) HolderStatus
}

// HolderStatus is what the doctor and the settings page say about a holder.
type HolderStatus struct {
	Kind string `json:"kind"`
	// Where says where the key is held (a path, a service address); never
	// the key.
	Where string `json:"where"`
	// Strength says in plain words what this holder does and does not
	// protect against.
	Strength string `json:"strength"`
	// Reachable is false when the holder cannot be used right now (a
	// service that does not answer).
	Reachable bool   `json:"reachable"`
	Problem   string `json:"problem,omitempty"`
}

// ErrNoRootKey is returned when something needs encryption at rest and no
// root key is configured.
var ErrNoRootKey = errors.New("no root key is configured, so nothing can be encrypted at rest: " + HowToCreateARootKey)

// HowToCreateARootKey is the instruction every refusal carries.
const HowToCreateARootKey = "create one with `launchway encryption init` (it writes a key file and tells you where to keep a copy), or in a chart install set encryption.keyFile.generate; then restart every server. See the guide \"Encryption at rest\""

// ErrWrongKey is returned at start when the database's keys do not open
// with the configured root key. Nothing is ever re-keyed by guessing.
type ErrWrongKey struct{ Detail string }

func (e *ErrWrongKey) Error() string {
	return "the configured root key does not open this database's encryption keys (" + e.Detail +
		"). This server will not start: it never re-keys by itself, because that would make the sealed secrets unreadable. " +
		"Configure the root key this database was set up with (a backup of it is the recovery); " +
		"if the key was replaced on purpose, supply the old one too as encryption.previous_key_file and run `launchway encryption rewrap`"
}

// localHolder is a root key held in this process: it wraps with AES-256-GCM.
type localHolder struct {
	kind  string
	where string
	why   string
	key   []byte
}

func (h *localHolder) Kind() string { return h.kind }

func (h *localHolder) Wrap(_ context.Context, key, aad []byte) ([]byte, error) {
	g, err := newGCM(h.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, key, aad), nil
}

func (h *localHolder) Unwrap(_ context.Context, wrapped, aad []byte) ([]byte, error) {
	g, err := newGCM(h.key)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < nonceLen+16 {
		return nil, errors.New("a wrapped key is damaged")
	}
	k, err := g.Open(nil, wrapped[:nonceLen], wrapped[nonceLen:], aad)
	if err != nil {
		return nil, errors.New("it does not open with this root key")
	}
	return k, nil
}

func (h *localHolder) Status(context.Context) HolderStatus {
	return HolderStatus{Kind: h.kind, Where: h.where, Strength: h.why, Reachable: true}
}

// Fingerprint-free by design: a root key has no identifier that could leak
// information about it; correctness is proved by unwrapping.

// NewRandomKey returns 32 random bytes.
func NewRandomKey() ([]byte, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("the system's random source failed: %w", err)
	}
	return k, nil
}

// Strength texts, honest about each holder.
const (
	strengthKeyFile = "A copy of the database or a backup reveals no secret. Whoever can read this file on the server, or the Kubernetes Secret that holds it, can open them. In a cluster the Secret is only as protected as the cluster's own secret encryption (etcd encryption), which Launchway cannot see."
	strengthEnv     = "A copy of the database reveals no secret. The key sits in the process environment, which administrators of the host or container runtime can read; prefer a key file or a service for production."
	strengthPhrase  = "A copy of the database reveals no secret, and the root key is not stored: it is unlocked by a passphrase at start. A server restarted unattended cannot start without the passphrase file."
	strengthTransit = "The root key never leaves the Vault or OpenBao transit service; every unwrap is logged there, and revoking the server's access makes every secret unreadable at once."
)
