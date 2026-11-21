package keyring

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

// DB is the part of the database the keyring needs: statements written with
// ? placeholders (the engine rebinds them for PostgreSQL) and, on SQLite,
// writes that are already serialized with the engine's other writers.
type DB interface {
	Exec(ctx context.Context, q string, a ...any) (sql.Result, error)
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, q string, a ...any) *sql.Row
}

// Sealer is what the code that stores a secret uses. *Keyring seals for
// real; Disabled is what a server without a root key has.
type Sealer interface {
	// Enabled says whether sealing is possible: a root key is in use.
	Enabled() bool
	Seal(ctx context.Context, b Binding, plain []byte) (string, error)
	Open(ctx context.Context, b Binding, sealed string) ([]byte, error)
}

// Disabled is the Sealer of a server with no root key. It refuses to seal
// and says how to fix that; it opens nothing.
type Disabled struct{}

func (Disabled) Enabled() bool { return false }
func (Disabled) Seal(context.Context, Binding, []byte) (string, error) {
	return "", ErrNoRootKey
}
func (Disabled) Open(context.Context, Binding, string) ([]byte, error) {
	return nil, errors.New("a stored value is sealed, but no root key is configured on this server: " + HowToCreateARootKey)
}

// Event is something the keyring did that an audit trail should record.
// Never key material.
type Event struct {
	Action  string // encryption.key_created, encryption.kek_rotated, ...
	Target  string
	Outcome string // ok or failed
	Detail  string
}

// Options for Open.
type Options struct {
	// Previous are holders that may unwrap keys the primary cannot: the old
	// root key during a replacement, or the old holder during a move to
	// another one. They are only ever used to read, and by Rewrap.
	Previous []Holder
	// Audit records an Event; nil: none.
	Audit func(context.Context, Event)
	Now   func() time.Time
}

type kekRow struct {
	Version   uint32
	Holder    string
	Wrapped   string
	CreatedAt int64
	Retired   int64
}

// DEKRow is a data key as stored (wrapped; never the key).
type DEKRow struct {
	Purpose, Org string
	Version      uint32
	KEKVersion   uint32
	CreatedAt    int64
}

type dekID struct {
	purpose, org string
	version      uint32
}

type latest struct {
	version uint32
	at      time.Time
}

// Keyring holds the unwrapped keys in memory and seals and opens values.
type Keyring struct {
	db           DB
	primary      Holder
	previous     []Holder
	installation string
	audit        func(context.Context, Event)
	now          func() time.Time

	mu       sync.Mutex
	keks     map[uint32][]byte
	curKEK   uint32
	deks     map[dekID][]byte
	latest   map[[2]string]latest
	rewrapNd bool // a key was opened only with a previous holder
}

// latestTTL is how long a server keeps using the data key version it last
// saw as newest before asking the database again, so a rotation made on
// another server is picked up without a restart.
const latestTTL = 10 * time.Second

func (k *Keyring) emit(ctx context.Context, e Event) {
	if k.audit != nil {
		k.audit(ctx, e)
	}
}

// Open unlocks the installation's keys with the root key. On the first
// start with a root key it creates the installation id and the first
// key-encryption key. If the keys in the database do not open with the root
// key, it returns *ErrWrongKey; it never creates new keys beside existing
// ones. Servers starting together are safe: each creation is one insert
// that one server wins, and the others read the winner's.
func Open(ctx context.Context, db DB, holder Holder, o Options) (*Keyring, error) {
	if holder == nil {
		return nil, ErrNoRootKey
	}
	k := &Keyring{db: db, primary: holder, previous: o.Previous, audit: o.Audit, now: o.Now,
		keks: map[uint32][]byte{}, deks: map[dekID][]byte{}, latest: map[[2]string]latest{}}
	if k.now == nil {
		k.now = time.Now
	}
	id, err := k.installationID(ctx)
	if err != nil {
		return nil, err
	}
	k.installation = id
	rows, err := k.listKEKs(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		if err := k.createKEK(ctx, 1); err != nil {
			return nil, err
		}
		k.emit(ctx, Event{Action: "encryption.key_created", Target: "installation", Outcome: "ok",
			Detail: "the installation's first key-encryption key was created under the " + holder.Kind() + " root key"})
		if rows, err = k.listKEKs(ctx); err != nil {
			return nil, err
		}
	}
	for _, r := range rows {
		if r.Retired != 0 {
			continue
		}
		key, viaPrevious, err := k.unwrapKEK(ctx, r)
		if err != nil {
			k.emit(ctx, Event{Action: "encryption.unseal", Target: fmt.Sprintf("kek/%d", r.Version), Outcome: "failed", Detail: err.Error()})
			return nil, err
		}
		k.keks[r.Version] = key
		k.rewrapNd = k.rewrapNd || viaPrevious
		if r.Version > k.curKEK {
			k.curKEK = r.Version
		}
	}
	if len(k.keks) == 0 {
		return nil, &ErrWrongKey{Detail: "every key-encryption key is retired"}
	}
	return k, nil
}

func (k *Keyring) installationID(ctx context.Context) (string, error) {
	var id string
	err := k.db.QueryRow(ctx, `SELECT value FROM encryption_state WHERE key = 'installation_id'`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	if _, err := k.db.Exec(ctx, `INSERT INTO encryption_state(key, value) VALUES('installation_id', ?) ON CONFLICT(key) DO NOTHING`,
		base64.RawURLEncoding.EncodeToString(b)); err != nil {
		return "", err
	}
	err = k.db.QueryRow(ctx, `SELECT value FROM encryption_state WHERE key = 'installation_id'`).Scan(&id)
	return id, err
}

// Installation is the installation's random id, bound into every sealed value.
func (k *Keyring) Installation() string { return k.installation }

func (k *Keyring) listKEKs(ctx context.Context) ([]kekRow, error) {
	rs, err := k.db.Query(ctx, `SELECT version, holder, wrapped, created_at, retired_at FROM encryption_keks ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []kekRow
	for rs.Next() {
		var r kekRow
		var v int64
		if err := rs.Scan(&v, &r.Holder, &r.Wrapped, &r.CreatedAt, &r.Retired); err != nil {
			return nil, err
		}
		r.Version = uint32(v)
		out = append(out, r)
	}
	return out, rs.Err()
}

func (k *Keyring) kekAAD(version uint32) []byte {
	return binary.BigEndian.AppendUint32(append([]byte("launchway/kek/v1\x00"), k.installation...), version)
}

func (k *Keyring) dekAAD(purpose, org string, version uint32) []byte {
	out := []byte("launchway/dek/v1\x00" + k.installation + "\x00" + purpose + "\x00" + org + "\x00")
	return binary.BigEndian.AppendUint32(out, version)
}

// createKEK makes key-encryption key `version` under the primary holder.
// If another server inserted that version first, theirs stands.
func (k *Keyring) createKEK(ctx context.Context, version uint32) error {
	key, err := NewRandomKey()
	if err != nil {
		return err
	}
	wrapped, err := k.primary.Wrap(ctx, key, k.kekAAD(version))
	if err != nil {
		return fmt.Errorf("the %s root key could not wrap a new key: %w", k.primary.Kind(), err)
	}
	_, err = k.db.Exec(ctx, `INSERT INTO encryption_keks(version, holder, wrapped, created_at) VALUES(?,?,?,?) ON CONFLICT(version) DO NOTHING`,
		int64(version), k.primary.Kind(), base64.RawStdEncoding.EncodeToString(wrapped), k.now().UnixMilli())
	return err
}

// unwrapKEK opens a stored key-encryption key with the primary holder, or
// with a previous one. viaPrevious says the primary could not.
func (k *Keyring) unwrapKEK(ctx context.Context, r kekRow) (key []byte, viaPrevious bool, err error) {
	wrapped, derr := base64.RawStdEncoding.DecodeString(r.Wrapped)
	if derr != nil {
		return nil, false, &ErrWrongKey{Detail: "a stored key is damaged"}
	}
	aad := k.kekAAD(r.Version)
	var kinds []string
	for i, h := range append([]Holder{k.primary}, k.previous...) {
		kinds = append(kinds, h.Kind())
		if h.Kind() != r.Holder {
			continue
		}
		if key, err := h.Unwrap(ctx, wrapped, aad); err == nil {
			return key, i > 0, nil
		}
	}
	for _, kind := range kinds {
		if kind == r.Holder {
			return nil, false, &ErrWrongKey{Detail: "key version " + fmt.Sprint(r.Version) + " was wrapped by a " + r.Holder + " root key, and the one configured is not it"}
		}
	}
	return nil, false, &ErrWrongKey{Detail: "this database's keys are held by a " + r.Holder + " root key, but this server is configured with a " + k.primary.Kind() + " one"}
}

// Enabled is true: a keyring is open.
func (k *Keyring) Enabled() bool { return true }

// NeedsRewrap says that a key-encryption key could be opened only with a
// previous holder, so `launchway encryption rewrap` has work to do.
func (k *Keyring) NeedsRewrap() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rewrapNd
}

func (k *Keyring) currentKEK() (uint32, []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.curKEK, k.keks[k.curKEK]
}

// kekByVersion returns a key-encryption key, reading newly created versions
// from the database (another server may have rotated).
func (k *Keyring) kekByVersion(ctx context.Context, v uint32) ([]byte, error) {
	k.mu.Lock()
	key, ok := k.keks[v]
	k.mu.Unlock()
	if ok {
		return key, nil
	}
	rows, err := k.listKEKs(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Version != v {
			continue
		}
		key, _, err := k.unwrapKEK(ctx, r)
		if err != nil {
			return nil, err
		}
		k.mu.Lock()
		k.keks[v] = key
		if v > k.curKEK && r.Retired == 0 {
			k.curKEK = v
		}
		k.mu.Unlock()
		return key, nil
	}
	return nil, fmt.Errorf("key-encryption key version %d is not in the database", v)
}

// dek returns the data key, unwrapping it from the database on first use.
func (k *Keyring) dek(ctx context.Context, purpose, org string, version uint32) ([]byte, error) {
	id := dekID{purpose, org, version}
	k.mu.Lock()
	key, ok := k.deks[id]
	k.mu.Unlock()
	if ok {
		return key, nil
	}
	var kv int64
	var wrapped string
	err := k.db.QueryRow(ctx, `SELECT kek_version, wrapped FROM encryption_deks WHERE purpose = ? AND org = ? AND version = ?`,
		purpose, org, int64(version)).Scan(&kv, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("the data key for %s version %d is not in the database (is this the database the value came from?)", purpose, version)
	}
	if err != nil {
		return nil, err
	}
	kek, err := k.kekByVersion(ctx, uint32(kv))
	if err != nil {
		return nil, err
	}
	raw, err := base64.RawStdEncoding.DecodeString(wrapped)
	if err != nil {
		return nil, errors.New("a stored data key is damaged")
	}
	key, err = (&localHolder{key: kek}).Unwrap(ctx, raw, k.dekAAD(purpose, org, version))
	if err != nil {
		return nil, errors.New("a stored data key does not open with its key-encryption key (the database was altered)")
	}
	k.mu.Lock()
	k.deks[id] = key
	k.mu.Unlock()
	return key, nil
}

// newestDEK returns the version to seal with, creating version 1 on first use.
func (k *Keyring) newestDEK(ctx context.Context, purpose, org string) (uint32, []byte, error) {
	lk := [2]string{purpose, org}
	k.mu.Lock()
	l, ok := k.latest[lk]
	k.mu.Unlock()
	if !ok || k.now().Sub(l.at) > latestTTL {
		var max sql.NullInt64
		if err := k.db.QueryRow(ctx, `SELECT MAX(version) FROM encryption_deks WHERE purpose = ? AND org = ?`, purpose, org).Scan(&max); err != nil {
			return 0, nil, err
		}
		v := uint32(max.Int64)
		if !max.Valid {
			if err := k.createDEK(ctx, purpose, org, 1); err != nil {
				return 0, nil, err
			}
			v = 1
		}
		l = latest{v, k.now()}
		k.mu.Lock()
		k.latest[lk] = l
		k.mu.Unlock()
	}
	key, err := k.dek(ctx, purpose, org, l.version)
	return l.version, key, err
}

func (k *Keyring) createDEK(ctx context.Context, purpose, org string, version uint32) error {
	key, err := NewRandomKey()
	if err != nil {
		return err
	}
	if mv, err := k.maxKEKVersion(ctx); err == nil && mv > 0 {
		if _, err := k.kekByVersion(ctx, mv); err == nil {
			k.mu.Lock()
			if mv > k.curKEK {
				k.curKEK = mv
			}
			k.mu.Unlock()
		}
	}
	kv, kek := k.currentKEK()
	wrapped, err := (&localHolder{key: kek}).Wrap(ctx, key, k.dekAAD(purpose, org, version))
	if err != nil {
		return err
	}
	_, err = k.db.Exec(ctx, `INSERT INTO encryption_deks(purpose, org, version, kek_version, wrapped, created_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(purpose, org, version) DO NOTHING`, purpose, org, int64(version), int64(kv), base64.RawStdEncoding.EncodeToString(wrapped), k.now().UnixMilli())
	return err
}

// Seal seals plain for the place b names, with the newest data key.
func (k *Keyring) Seal(ctx context.Context, b Binding, plain []byte) (string, error) {
	if b.Purpose == "" || b.Table == "" || b.Column == "" || b.Row == "" {
		return "", errors.New("a sealed value must name its purpose, table, column and row")
	}
	v, key, err := k.newestDEK(ctx, b.Purpose, b.Org)
	if err != nil {
		return "", err
	}
	return sealWith(key, v, b.aad(k.installation, v), plain)
}

// Open opens a value sealed for the place b names. A value moved to another
// row, column, table or organization does not open.
func (k *Keyring) Open(ctx context.Context, b Binding, sealed string) ([]byte, error) {
	v, raw, err := parse(sealed)
	if err != nil {
		return nil, err
	}
	key, err := k.dek(ctx, b.Purpose, b.Org, v)
	if err != nil {
		return nil, err
	}
	return openWith(key, b.aad(k.installation, v), raw)
}

// Zero overwrites a plaintext secret once it is no longer needed.
func Zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
