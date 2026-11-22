package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// DB is what the signing store needs of a database (the SQL is in internal/store).
type DB = store.CtxDB

// Errors; test with errors.Is.
var (
	ErrNotFound = errors.New("no such signing key")
	ErrExists   = errors.New("that key is already registered")
	ErrInvalid  = errors.New("invalid signing key")
)

// MaxKeysPerPerson bounds what one account may register.
const MaxKeysPerPerson = 50

// MinRSABits is the shortest RSA key accepted.
const MinRSABits = 3072

// InstallationOwner is the signer name of the installation's own key.
const InstallationOwner = "launchway"

// Sealer seals the installation key's private half (internal/sealing's Box).
type Sealer interface {
	Seal(ctx context.Context, row string, plain []byte) (string, error)
	Open(ctx context.Context, row, stored string) ([]byte, error)
}

// Store is the signing tables.
type Store struct {
	db  DB
	now func() time.Time
}

// NewStore makes a store over a database.
func NewStore(db DB) *Store { return &Store{db: db, now: time.Now} }

// SetClock replaces the clock, for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMs(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
}

// ParseKey reads a public key a person pasted and says what it is. Only SSH
// public keys can be registered today.
func ParseKey(text string) (ssh.PublicKey, string, error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "BEGIN PGP") {
		return nil, "", fmt.Errorf("%w: OpenPGP keys cannot be registered yet; sign with an SSH key (git config gpg.format ssh)", ErrInvalid)
	}
	if strings.Count(text, "\n") > 0 || text == "" || len(text) > 8192 {
		return nil, "", fmt.Errorf("%w: paste one line from a .pub file", ErrInvalid)
	}
	pk, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		return nil, "", fmt.Errorf("%w: that is not an SSH public key (%v)", ErrInvalid, err)
	}
	if _, isCert := pk.(*ssh.Certificate); isCert {
		return nil, "", fmt.Errorf("%w: certificates cannot be registered; register the key itself", ErrInvalid)
	}
	if pk.Type() == ssh.KeyAlgoRSA {
		if cp, ok := pk.(ssh.CryptoPublicKey); ok {
			if rk, ok := cp.CryptoPublicKey().(interface{ Size() int }); ok && rk.Size()*8 < MinRSABits {
				return nil, "", fmt.Errorf("%w: RSA keys must be at least %d bits", ErrInvalid, MinRSABits)
			}
		}
	}
	return pk, comment, nil
}

// NewKey is a key to register.
type NewKey struct {
	Owner     string
	Name      string
	PublicKey string
	ExpiresAt time.Time
}

func keyOf(r store.SigningKeyRow) Key {
	k := Key{ID: r.ID, Owner: r.Owner, Kind: r.Kind, Name: r.Name, Fingerprint: r.Fingerprint, PublicKey: r.PublicText, CreatedAt: fromMs(r.CreatedAt), ExpiresAt: fromMs(r.ExpiresAt), RevokedAt: fromMs(r.RevokedAt)}
	k.Type, _, _ = strings.Cut(k.PublicKey, " ")
	return k
}

// Add registers a signing key for a person.
func (s *Store) Add(ctx context.Context, n NewKey) (Key, error) {
	pk, comment, err := ParseKey(n.PublicKey)
	if err != nil {
		return Key{}, err
	}
	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = comment
	}
	if name == "" || len(name) > 100 || strings.ContainsAny(name, "\x00\n\r") {
		return Key{}, fmt.Errorf("%w: give the key a name of up to 100 characters", ErrInvalid)
	}
	if !n.ExpiresAt.IsZero() && !n.ExpiresAt.After(s.now()) {
		return Key{}, fmt.Errorf("%w: the expiry is in the past", ErrInvalid)
	}
	have, err := store.CountSigningKeys(ctx, s.db, n.Owner)
	if err != nil {
		return Key{}, err
	}
	if have >= MaxKeysPerPerson {
		return Key{}, fmt.Errorf("%w: at most %d signing keys per person", ErrInvalid, MaxKeysPerPerson)
	}
	fp := ssh.FingerprintSHA256(pk)
	if _, ok, err := s.ByFingerprint(ctx, fp); err != nil {
		return Key{}, err
	} else if ok {
		return Key{}, fmt.Errorf("%w: a key can belong to one person only", ErrExists)
	}
	id := ids.New("sgk")
	if err := store.InsertSigningKey(ctx, s.db, store.SigningKeyRow{ID: id, Owner: n.Owner, Kind: "ssh", Name: name, Fingerprint: fp,
		PublicText: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk))), CreatedAt: ms(s.now()), ExpiresAt: ms(n.ExpiresAt)}); err != nil {
		return Key{}, err
	}
	return s.Get(ctx, id)
}

// Get returns a key by ID.
func (s *Store) Get(ctx context.Context, id string) (Key, error) {
	r, ok, err := store.SigningKey(ctx, s.db, id)
	if err != nil {
		return Key{}, err
	}
	if !ok {
		return Key{}, ErrNotFound
	}
	return keyOf(r), nil
}

// ByFingerprint finds a key.
func (s *Store) ByFingerprint(ctx context.Context, fp string) (Key, bool, error) {
	r, ok, err := store.SigningKeyByFingerprint(ctx, s.db, fp)
	if err != nil || !ok {
		return Key{}, false, err
	}
	return keyOf(r), true, nil
}

// List returns a person's keys.
func (s *Store) List(ctx context.Context, owner string) ([]Key, error) {
	rows, err := store.SigningKeysOf(ctx, s.db, owner)
	if err != nil {
		return nil, err
	}
	out := []Key{}
	for _, r := range rows {
		out = append(out, keyOf(r))
	}
	return out, nil
}

// Revoke marks a key revoked from now on: objects dated before stay verified.
func (s *Store) Revoke(ctx context.Context, id string) error {
	changed, err := store.RevokeSigningKey(ctx, s.db, id, ms(s.now()))
	if err != nil || changed {
		return err
	}
	_, err = s.Get(ctx, id) // already revoked is fine; a key that is not there is not
	return err
}

// Delete removes a key: what it signed becomes "signed by an unknown key".
func (s *Store) Delete(ctx context.Context, id string) error {
	ok, err := store.DeleteSigningKey(ctx, s.db, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// GetCached returns the cached cryptographic verdict of an object.
func (s *Store) GetCached(ctx context.Context, object string) (Cached, bool) {
	r, ok := store.SignatureCached(ctx, s.db, object)
	if !ok {
		return Cached{}, false
	}
	return Cached{Object: object, SigType: r.SigType, Fingerprint: r.Fingerprint, CryptoOK: r.CryptoOK, Reason: r.Reason}, true
}

// PutCached remembers it; a failure to write only costs a recomputation.
func (s *Store) PutCached(ctx context.Context, c Cached) {
	store.PutSignatureCache(ctx, s.db, store.SignatureCacheRow{Object: c.Object, SigType: c.SigType, Fingerprint: c.Fingerprint, CryptoOK: c.CryptoOK, Reason: c.Reason}, ms(s.now()))
}

// installationRow is the id of the one row of signing_installation.
const installationRow = "installation"

// InstallationSigner returns the installation's signing key, making it the
// first time. The private half is stored sealed, bound to its row; with no
// sealer the key cannot be made and an error says so.
func (s *Store) InstallationSigner(ctx context.Context, seal Sealer) (ssh.Signer, error) {
	row, found, err := store.InstallationKey(ctx, s.db, installationRow)
	if err != nil {
		return nil, err
	}
	if !found {
		if seal == nil {
			return nil, errors.New("the installation's signing key cannot be made: this server has no root key to seal it with")
		}
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, gerr
		}
		sg, gerr := ssh.NewSignerFromKey(priv)
		if gerr != nil {
			return nil, gerr
		}
		pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sg.PublicKey())))
		if gerr = store.InsertInstallationKey(ctx, s.db, seal, installationRow, "ed25519", pub, ssh.FingerprintSHA256(sg.PublicKey()), priv.Seed(), ms(s.now())); gerr != nil {
			return nil, gerr
		}
		// Servers that start together each make one and the first insert wins: everyone reads back the winner's.
		if row, found, err = store.InstallationKey(ctx, s.db, installationRow); err != nil || !found {
			return nil, errors.New("the installation's signing key could not be read back")
		}
	}
	if seal == nil {
		return nil, errors.New("the installation's signing key cannot be opened: this server has no root key")
	}
	seed, err := seal.Open(ctx, installationRow, row.Sealed)
	if err != nil {
		return nil, fmt.Errorf("the installation's signing key cannot be opened with this server's key (was the root key changed?): %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("the installation's signing key is damaged")
	}
	return ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(seed))
}

// InstallationKey is the public half of the installation's signing key.
func (s *Store) InstallationKey(ctx context.Context) (Key, bool) {
	r, ok, err := store.InstallationKey(ctx, s.db, installationRow)
	if err != nil || !ok {
		return Key{}, false
	}
	return Key{ID: installationRow, Owner: InstallationOwner, Kind: "ssh", Name: "Launchway installation key", Type: "ssh-ed25519",
		Fingerprint: r.Fingerprint, PublicKey: r.PublicText, CreatedAt: fromMs(r.CreatedAt), Installation: true}, true
}
