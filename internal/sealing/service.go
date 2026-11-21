package sealing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/keyring"
)

// DB is the database as the engine offers it (engine.Database).
type DB interface {
	keyring.DB
	// WriteLock serializes this process's writers on SQLite (a no-op on PostgreSQL).
	WriteLock() func()
	Postgres() bool
}

// locked takes the write lock around every statement that may write, so the
// keyring and the migration queue with the engine's other writers on SQLite.
type locked struct{ DB }

func (l locked) Exec(ctx context.Context, q string, a ...any) (sql.Result, error) {
	defer l.WriteLock()()
	return l.DB.Exec(ctx, q, a...)
}

// Options for Start.
type Options struct {
	DB DB
	// Holder is the root key holder; nil means no root key is configured.
	Holder keyring.Holder
	// Previous holders may unwrap keys the Holder cannot (root key replacement).
	Previous []keyring.Holder
	Log      *slog.Logger
	// Audit records key creation, rotation and failed unsealing; nil: none.
	Audit func(context.Context, keyring.Event)
	Now   func() time.Time
	// Instance names this server in the migration lease.
	Instance string
	// Notes are warnings the caller knows and the service cannot (for example
	// that the key file lies in the data directory); the report shows them.
	Notes []string
	// Wait bounds how long a starting server waits for another to finish
	// sealing what was stored in the clear (default 60 seconds).
	Wait time.Duration
	// OlderServers counts servers of an older release still running against
	// this database (nil: do not look). With it and QuietFor set, a server
	// that finds stored secrets in the clear does not seal them at start: it
	// serves, and seals once no older server has been seen for QuietFor, so
	// that no older server, which cannot read a sealed value, ever meets one
	// during a rolling update. See docs/guide/upgrading.md.
	OlderServers func(ctx context.Context) (int, error)
	// QuietFor is how long no older server must be seen (default 0: seal at
	// start, as before; the server sets 45 seconds).
	QuietFor time.Duration
	// Poll is how often older servers are looked for while waiting (default 3 seconds).
	Poll time.Duration
}

// Service seals and opens the server's stored secrets.
type Service struct {
	db       DB
	kdb      keyring.DB
	kr       *keyring.Keyring
	keys     keyring.Sealer
	log      *slog.Logger
	now      func() time.Time
	instance string
	wait     time.Duration
	auditFn  func(context.Context, keyring.Event)
	notes    []string
	// Set by Inspect: a root key is configured but unused, or the database
	// has not been upgraded to the schema with the key tables.
	configured  string
	notUpgraded bool
	passes      atomic.Int64 // migration passes this process ran (tests)
	olderFn     func(context.Context) (int, error)
	quietFor    time.Duration
	poll        time.Duration
	// sealedRows counts the rows this process sealed (tests).
	sealedRows atomic.Int64
}

// ErrDatabaseEncrypted is the refusal to start a server with no root key
// against a database that is encrypted at rest.
var ErrDatabaseEncrypted = errors.New("this database is encrypted at rest (it holds wrapped encryption keys), but no root key is configured on this server. " +
	"A server never starts without the key and never guesses: configure the root key this database was set up with " +
	"(encryption.key_file in the bootstrap file or LAUNCHWAY_ROOT_KEY_FILE, LAUNCHWAY_ROOT_KEY, a passphrase keystore, or the Vault transit settings; " +
	"the Helm chart mounts it from the Secret named in encryption.keyFile.existingSecret). A backup of the database is useless without that key, by design")

// Start opens the keyring with the configured root key and, on first start
// with one, seals what was stored in the clear. With no root key it returns a
// service in "not encrypted at rest" mode, unless the database is already
// encrypted, which is refused.
func Start(ctx context.Context, o Options) (*Service, error) {
	s := &Service{db: o.DB, kdb: locked{o.DB}, log: o.Log, now: o.Now, instance: o.Instance, wait: o.Wait, auditFn: o.Audit, notes: o.Notes,
		olderFn: o.OlderServers, quietFor: o.QuietFor, poll: o.Poll}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.wait == 0 {
		s.wait = 60 * time.Second
	}
	if s.poll == 0 {
		s.poll = 3 * time.Second
	}
	if s.instance == "" {
		s.instance = "server"
	}
	if o.Holder == nil {
		var n int
		if err := o.DB.QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&n); err != nil {
			return nil, fmt.Errorf("reading the encryption state: %w", err)
		}
		if n > 0 {
			return nil, ErrDatabaseEncrypted
		}
		s.keys = keyring.Disabled{}
		return s, nil
	}
	kr, err := keyring.Open(ctx, s.kdb, o.Holder, keyring.Options{Previous: o.Previous, Audit: o.Audit, Now: o.Now})
	if err != nil {
		return nil, err
	}
	s.kr, s.keys = kr, kr
	if deferred, err := s.deferSealing(ctx); err != nil {
		return nil, err
	} else if deferred {
		return s, nil
	}
	if err := s.migrateAtStart(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Inspect is Start for a tool that only reads (`launchway doctor`): it never
// creates keys and never seals anything. A root key that is configured but
// has not been used yet is reported as such; one that is in use is opened, so
// a wrong key shows.
func Inspect(ctx context.Context, o Options) (*Service, error) {
	s := &Service{db: o.DB, kdb: locked{o.DB}, log: o.Log, now: o.Now, instance: o.Instance, wait: o.Wait, auditFn: o.Audit, notes: o.Notes}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.now == nil {
		s.now = time.Now
	}
	s.keys = keyring.Disabled{}
	var n int
	if err := o.DB.QueryRow(ctx, `SELECT count(*) FROM encryption_keks`).Scan(&n); err != nil {
		if isMissingTable(err) {
			s.notUpgraded = true
			return s, nil // not yet migrated to the schema that has the key tables
		}
		return nil, fmt.Errorf("reading the encryption state: %w", err)
	}
	if o.Holder != nil {
		s.configured = o.Holder.Kind()
	}
	switch {
	case n == 0:
		return s, nil
	case o.Holder == nil:
		return nil, ErrDatabaseEncrypted
	}
	kr, err := keyring.Open(ctx, s.kdb, o.Holder, keyring.Options{Previous: o.Previous, Now: o.Now})
	if err != nil {
		return nil, err
	}
	s.kr, s.keys = kr, kr
	return s, nil
}

// Encrypted says whether a root key is in use.
func (s *Service) Encrypted() bool { return s.kr != nil }

// Keys is the sealer: the keyring, or one that refuses and says how to get a root key.
func (s *Service) Keys() keyring.Sealer { return s.keys }

// Keyring returns the keyring, nil when there is no root key.
func (s *Service) Keyring() *keyring.Keyring { return s.kr }

// RequireRootKey returns the plain refusal a feature that adds new secrets
// gives when there is no root key. what names the feature.
func (s *Service) RequireRootKey(what string) error {
	if s.Encrypted() {
		return nil
	}
	return fmt.Errorf("%s cannot be turned on yet: it stores secrets, and this server has no root key, so it would keep them in the clear. %s", what, keyring.HowToCreateARootKey)
}

// ---- state ----

func (s *Service) state(ctx context.Context, key string) (string, bool) {
	var v string
	if err := s.db.QueryRow(ctx, `SELECT value FROM encryption_state WHERE key = ?`, key).Scan(&v); err != nil {
		return "", false
	}
	return v, true
}

func (s *Service) setState(ctx context.Context, key, value string) error {
	_, err := s.kdb.Exec(ctx, `INSERT INTO encryption_state(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// complete says whether every registered secret has been sealed, after which
// a stored value in the clear is refused, not accepted: otherwise anything
// that could write a plaintext value into a sealed column would be believed.
func (s *Service) complete(ctx context.Context) bool {
	v, ok := s.state(ctx, "sealed_complete")
	return ok && v == "1"
}

func millis(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

// ---- shared values ----

const metaRowPrefix = "shared:"

// SharedValue returns the value stored under key (storing proposed first if
// there is none), the way Engine.SharedValue does, but sealed at rest when a
// root key is in use. It is for secrets; every server sharing the database
// gets the same value.
func (s *Service) SharedValue(key, proposed string) (string, error) {
	return s.SharedValueContext(context.Background(), key, proposed)
}

// SharedValueContext is SharedValue for a caller that has a context.
func (s *Service) SharedValueContext(ctx context.Context, key, proposed string) (string, error) {
	row := metaRowPrefix + key
	stored := proposed
	if s.Encrypted() {
		sealed, err := s.keys.Seal(ctx, metaBinding(row), []byte(proposed))
		if err != nil {
			return "", err
		}
		stored = sealed
	}
	if _, err := s.kdb.Exec(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING`, row, stored); err != nil {
		return "", err
	}
	var v string
	if err := s.db.QueryRow(ctx, `SELECT value FROM meta WHERE key = ?`, row).Scan(&v); err != nil {
		return "", err
	}
	plain, err := s.openStored(ctx, metaColumn, row, v)
	return string(plain), err
}

// ReadShared returns a shared value if it exists, without creating it.
func (s *Service) ReadShared(ctx context.Context, key string) (string, bool, error) {
	row := metaRowPrefix + key
	var v string
	err := s.db.QueryRow(ctx, `SELECT value FROM meta WHERE key = ?`, row).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plain, err := s.openStored(ctx, metaColumn, row, v)
	return string(plain), err == nil, err
}

func metaBinding(row string) keyring.Binding {
	return keyring.Binding{Purpose: keyring.PurposeTokens, Table: "meta", Column: "value", Row: row}
}

// openStored opens a stored value: sealed ones through the keyring; one in
// the clear only from before encryption was turned on (read through the
// column's legacy decoder), and never once everything has been sealed.
func (s *Service) openStored(ctx context.Context, c Column, row, stored string) ([]byte, error) {
	if keyring.IsSealed(stored) {
		return s.keys.Open(ctx, c.binding(row), stored)
	}
	if s.Encrypted() && s.complete(ctx) {
		return nil, fmt.Errorf("%s.%s of %q is stored in the clear in an installation whose secrets are all sealed: refusing to use it (something wrote it without the root key)", c.Table, c.Column, row)
	}
	return c.legacyOpen(ctx, s, stored)
}
