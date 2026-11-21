package sshkeys

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/ids"
)

// DB is what the store needs of a database: the engine's Database satisfies it.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Errors; test with errors.Is.
var (
	ErrNotFound = errors.New("no such key")
	ErrExists   = errors.New("that key is already registered")
	ErrInvalid  = errors.New("invalid key")
)

// Kinds of key.
const (
	User   = "user"
	Deploy = "deploy"
)

// MinRSABits is the shortest RSA key accepted.
const MinRSABits = 3072

// Key is a registered public key.
type Key struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Owner        string    `json:"owner,omitempty"`
	RepoID       string    `json:"repository_id,omitempty"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Fingerprint  string    `json:"fingerprint"`
	PublicKey    string    `json:"public_key"`
	CanWrite     bool      `json:"can_write,omitempty"`
	Groups       []string  `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	CreatedBy    string    `json:"created_by,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	LastUsedAt   time.Time `json:"last_used_at,omitzero"`
	LastUsedFrom string    `json:"last_used_from,omitempty"`
}

// Expired reports whether the key's expiry has passed.
func (k Key) Expired(now time.Time) bool { return !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt) }

// NewKey is a key to register.
type NewKey struct {
	Kind      string
	Owner     string // a user key's owner
	RepoID    string // a deploy key's repository
	Name      string
	PublicKey string // authorized_keys format
	CanWrite  bool   // deploy keys only: may push
	Groups    []string
	CreatedBy string
	ExpiresAt time.Time
	// Now is the time of the request (default: the store's clock).
	Now time.Time
}

// Store is the keys' tables.
type Store struct {
	db  DB
	now func() time.Time
}

// New makes a store over a database.
func New(db DB) *Store { return &Store{db: db, now: time.Now} }

// SetClock replaces the clock; for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Parse reads one public key in authorized_keys format and refuses what the
// server will not accept, with the reason.
func Parse(text string) (ssh.PublicKey, string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, "", fmt.Errorf("%w: paste a public key (the line from a .pub file)", ErrInvalid)
	}
	if strings.Contains(text, "PRIVATE KEY") {
		return nil, "", fmt.Errorf("%w: that is a private key; never share it. Paste the public key (the .pub file) instead", ErrInvalid)
	}
	if strings.ContainsAny(text, "\r\n") {
		return nil, "", fmt.Errorf("%w: add one key at a time", ErrInvalid)
	}
	fields := strings.Fields(text)
	if len(fields) > 0 && strings.HasPrefix(fields[0], "ssh-dss") {
		return nil, "", fmt.Errorf("%w: DSA keys (ssh-dss) are obsolete and weak; create an ed25519 key with ssh-keygen -t ed25519", ErrInvalid)
	}
	key, comment, options, _, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		return nil, "", fmt.Errorf("%w: this is not a public key in the OpenSSH format (ssh-ed25519 AAAA... name)", ErrInvalid)
	}
	if len(options) > 0 {
		return nil, "", fmt.Errorf("%w: key options such as command= or from= are not supported; leave them out", ErrInvalid)
	}
	if _, ok := key.(*ssh.Certificate); ok {
		return nil, "", fmt.Errorf("%w: that is a certificate; register the public key it was made for, or ask the administrator to trust your certificate authority", ErrInvalid)
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoSKED25519, ssh.KeyAlgoSKECDSA256:
	case ssh.KeyAlgoRSA:
		if ck, ok := key.(ssh.CryptoPublicKey); ok {
			if pk, ok := ck.CryptoPublicKey().(*rsa.PublicKey); ok && pk.N.BitLen() < MinRSABits {
				return nil, "", fmt.Errorf("%w: this RSA key has %d bits and at least %d are required; create an ed25519 key with ssh-keygen -t ed25519", ErrInvalid, pk.N.BitLen(), MinRSABits)
			}
		}
	default:
		return nil, "", fmt.Errorf("%w: %s keys are not accepted; use ed25519, ECDSA or RSA of at least %d bits", ErrInvalid, key.Type(), MinRSABits)
	}
	return key, comment, nil
}

const cols = `id, kind, owner, repo_id, name, key_type, public, fingerprint, can_write, groups, created_at, created_by, expires_at, last_used_at, last_used_from`

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func scan(sc interface{ Scan(...any) error }) (Key, error) {
	var k Key
	var w int
	var groups string
	var created, exp, used int64
	if err := sc.Scan(&k.ID, &k.Kind, &k.Owner, &k.RepoID, &k.Name, &k.Type, &k.PublicKey, &k.Fingerprint, &w, &groups, &created, &k.CreatedBy, &exp, &used, &k.LastUsedFrom); err != nil {
		return k, err
	}
	k.CanWrite = w != 0
	_ = json.Unmarshal([]byte(groups), &k.Groups)
	k.CreatedAt, k.ExpiresAt, k.LastUsedAt = fromMS(created), fromMS(exp), fromMS(used)
	return k, nil
}

// Add registers a key. A key already registered, as anyone's, is refused.
func (s *Store) Add(ctx context.Context, n NewKey) (Key, error) {
	if n.Kind != User && n.Kind != Deploy {
		return Key{}, fmt.Errorf("%w: unknown kind %q", ErrInvalid, n.Kind)
	}
	if n.Kind == User && n.Owner == "" || n.Kind == Deploy && n.RepoID == "" {
		return Key{}, fmt.Errorf("%w: a key needs an owner", ErrInvalid)
	}
	n.Name = strings.TrimSpace(n.Name)
	pub, comment, err := Parse(n.PublicKey)
	if err != nil {
		return Key{}, err
	}
	if n.Name == "" {
		n.Name = comment
	}
	if n.Name == "" {
		return Key{}, fmt.Errorf("%w: give the key a name, such as the computer it is on", ErrInvalid)
	}
	if len(n.Name) > 100 {
		return Key{}, fmt.Errorf("%w: the name is longer than 100 characters", ErrInvalid)
	}
	now := n.Now
	if now.IsZero() {
		now = s.now()
	}
	if !n.ExpiresAt.IsZero() && !n.ExpiresAt.After(now) {
		return Key{}, fmt.Errorf("%w: the expiry is in the past", ErrInvalid)
	}
	groups, _ := json.Marshal(n.Groups)
	if n.Groups == nil {
		groups = []byte("[]")
	}
	k := Key{ID: ids.New("key"), Kind: n.Kind, Owner: n.Owner, RepoID: n.RepoID, Name: n.Name, Type: pub.Type(),
		Fingerprint: ssh.FingerprintSHA256(pub), PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))),
		CanWrite: n.CanWrite && n.Kind == Deploy, Groups: n.Groups, CreatedAt: now.UTC(), CreatedBy: n.CreatedBy, ExpiresAt: n.ExpiresAt.UTC()}
	_, err = s.db.Exec(ctx, `INSERT INTO ssh_keys(`+cols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,0,'')`,
		k.ID, k.Kind, k.Owner, k.RepoID, k.Name, k.Type, k.PublicKey, k.Fingerprint, b2i(k.CanWrite), string(groups), ms(now), k.CreatedBy, ms(k.ExpiresAt))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return Key{}, fmt.Errorf("%w: a key belongs to one owner, and %s is registered already", ErrExists, k.Fingerprint)
		}
		return Key{}, err
	}
	return k, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// List returns a person's keys (kind User, owner) or a repository's deploy
// keys (kind Deploy, owner = repository ID), oldest first.
func (s *Store) List(ctx context.Context, kind, owner string) ([]Key, error) {
	q, arg := `owner = ?`, owner
	if kind == Deploy {
		q = `repo_id = ?`
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM ssh_keys WHERE kind = ? AND `+q+` ORDER BY created_at, id`, kind, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Key{}
	for rows.Next() {
		k, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Get returns a key by ID.
func (s *Store) Get(ctx context.Context, id string) (Key, error) {
	k, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM ssh_keys WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// ByFingerprint finds the key with a SHA256 fingerprint.
func (s *Store) ByFingerprint(ctx context.Context, fp string) (Key, error) {
	k, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM ssh_keys WHERE fingerprint = ?`, fp))
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// Delete removes a key.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.Exec(ctx, `DELETE FROM ssh_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteForRepo removes a repository's deploy keys (when it is deleted).
func (s *Store) DeleteForRepo(ctx context.Context, repoID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM ssh_keys WHERE kind = ? AND repo_id = ?`, Deploy, repoID)
	return err
}

// Touch records that a key was used, from an address. It writes at most once
// a minute per key.
func (s *Store) Touch(ctx context.Context, id string, from string, now time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE ssh_keys SET last_used_at = ?, last_used_from = ? WHERE id = ? AND last_used_at < ?`,
		ms(now), from, id, ms(now.Add(-time.Minute)))
	return err
}
