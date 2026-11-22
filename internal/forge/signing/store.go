package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

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

const keyCols = `id, owner, kind, name, fingerprint, public_text, created_at, expires_at, revoked_at`

func scanKey(r interface{ Scan(...any) error }) (Key, error) {
	var k Key
	var c, e, v int64
	if err := r.Scan(&k.ID, &k.Owner, &k.Kind, &k.Name, &k.Fingerprint, &k.PublicKey, &c, &e, &v); err != nil {
		return Key{}, err
	}
	k.CreatedAt, k.ExpiresAt, k.RevokedAt = fromMs(c), fromMs(e), fromMs(v)
	k.Type, _, _ = strings.Cut(k.PublicKey, " ")
	return k, nil
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
	var have int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM signing_keys WHERE owner = ? AND kind = 'ssh'`, n.Owner).Scan(&have); err != nil {
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
	text := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
	id := ids.New("sgk")
	if _, err := s.db.Exec(ctx, `INSERT INTO signing_keys(`+keyCols+`) VALUES(?,?,?,?,?,?,?,?,0)`,
		id, n.Owner, "ssh", name, fp, text, ms(s.now()), ms(n.ExpiresAt)); err != nil {
		return Key{}, err
	}
	return s.Get(ctx, id)
}

// Get returns a key by ID.
func (s *Store) Get(ctx context.Context, id string) (Key, error) {
	k, err := scanKey(s.db.QueryRow(ctx, `SELECT `+keyCols+` FROM signing_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, ErrNotFound
	}
	return k, err
}

// ByFingerprint finds a key.
func (s *Store) ByFingerprint(ctx context.Context, fp string) (Key, bool, error) {
	k, err := scanKey(s.db.QueryRow(ctx, `SELECT `+keyCols+` FROM signing_keys WHERE fingerprint = ?`, fp))
	if errors.Is(err, sql.ErrNoRows) {
		return Key{}, false, nil
	}
	return k, err == nil, err
}

// List returns a person's keys.
func (s *Store) List(ctx context.Context, owner string) ([]Key, error) {
	rows, err := s.db.Query(ctx, `SELECT `+keyCols+` FROM signing_keys WHERE owner = ? ORDER BY created_at, id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Revoke marks a key revoked from now on: objects dated before stay verified.
func (s *Store) Revoke(ctx context.Context, id string) error {
	res, err := s.db.Exec(ctx, `UPDATE signing_keys SET revoked_at = ? WHERE id = ? AND revoked_at = 0`, ms(s.now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// Delete removes a key: what it signed becomes "signed by an unknown key".
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM signing_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetCached returns the cached cryptographic verdict of an object.
func (s *Store) GetCached(ctx context.Context, object string) (Cached, bool) {
	c := Cached{Object: object}
	var ok int
	err := s.db.QueryRow(ctx, `SELECT sig_type, key_fingerprint, crypto_ok, reason FROM signature_cache WHERE object_sha = ?`, object).Scan(&c.SigType, &c.Fingerprint, &ok, &c.Reason)
	if err != nil {
		return Cached{}, false
	}
	c.CryptoOK = ok != 0
	return c, true
}

// PutCached remembers it; a failure to write only costs a recomputation.
func (s *Store) PutCached(ctx context.Context, c Cached) {
	ok := 0
	if c.CryptoOK {
		ok = 1
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO signature_cache(object_sha, sig_type, key_fingerprint, crypto_ok, reason, checked_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(object_sha) DO NOTHING`, c.Object, c.SigType, c.Fingerprint, ok, c.Reason, ms(s.now()))
}

// installationRow is the id of the one row of signing_installation.
const installationRow = "installation"

// InstallationSigner returns the installation's signing key, making it the
// first time. The private half is stored sealed, bound to its row; with no
// sealer the key cannot be made and an error says so.
func (s *Store) InstallationSigner(ctx context.Context, seal Sealer) (ssh.Signer, error) {
	var sealed string
	err := s.db.QueryRow(ctx, `SELECT sealed FROM signing_installation WHERE id = ?`, installationRow).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
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
		blob, gerr := seal.Seal(ctx, installationRow, priv.Seed())
		if gerr != nil {
			return nil, gerr
		}
		pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sg.PublicKey())))
		if _, gerr = s.db.Exec(ctx, `INSERT INTO signing_installation(id, algo, public_text, fingerprint, sealed, created_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(id) DO NOTHING`, installationRow, "ed25519", pub, ssh.FingerprintSHA256(sg.PublicKey()), blob, ms(s.now())); gerr != nil {
			return nil, gerr
		}
		err = s.db.QueryRow(ctx, `SELECT sealed FROM signing_installation WHERE id = ?`, installationRow).Scan(&sealed)
	}
	if err != nil {
		return nil, err
	}
	if seal == nil {
		return nil, errors.New("the installation's signing key cannot be opened: this server has no root key")
	}
	seed, err := seal.Open(ctx, installationRow, sealed)
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
	var pub, fp string
	var at int64
	if err := s.db.QueryRow(ctx, `SELECT public_text, fingerprint, created_at FROM signing_installation WHERE id = ?`, installationRow).Scan(&pub, &fp, &at); err != nil {
		return Key{}, false
	}
	return Key{ID: installationRow, Owner: InstallationOwner, Kind: "ssh", Name: "Launchway installation key", Type: "ssh-ed25519",
		Fingerprint: fp, PublicKey: pub, CreatedAt: fromMs(at), Installation: true}, true
}
