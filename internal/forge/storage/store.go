package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Options configure a Store.
type Options struct {
	// DB is the engine's database: the refs and the pack manifest live there.
	DB *engine.Database
	// Blobs holds the packs. Every server that shares DB must share it.
	Blobs blobstore.Store
	// CacheDir is where this server keeps its disposable caches. Nothing in
	// it matters: it can be deleted at any time.
	CacheDir string
	// Server names this server (the holder of maintenance leases).
	Server string
	// Binary is the executable git's hooks link to: the launchway binary, which
	// answers the hooks (see gitexec.RegisterEntry). Default os.Executable.
	Binary string
	// NoVerifyUpload turns off reading every uploaded pack back to check its
	// SHA-256 before the push can commit. Leave it on unless the blob store
	// checks content on write: a store that reports success and keeps something
	// else would otherwise make an acknowledged push unfetchable.
	NoVerifyUpload bool
	// MaxRepoBytes caps what one repository's live packs may total (0: no cap).
	MaxRepoBytes int64
	// MaxPackBytes caps one push's pack; the database blob store's largest
	// object (artifacts.database.max_blob_mib) is the usual value (0: no cap).
	MaxPackBytes int64
	// WarnFraction is when Usage starts warning, as a fraction of a cap
	// (default 0.8).
	WarnFraction float64
	// Limits is the memory, CPU and file-size ceiling of every git process.
	Limits gitexec.Limits
	// Log receives warnings; default slog.Default.
	Log *slog.Logger
}

// Store hosts repositories.
type Store struct {
	o  Options
	l  *Ledger
	lg *slog.Logger
	// beforeDownload lets a test retire a pack under a sync that is about to
	// download it.
	beforeDownload func(Pack)
}

// ErrWrongStore means this server's blob store is not the one the database was
// set up with. The server refuses to host: it would split the repositories.
var ErrWrongStore = errors.New("this server's repository store is not the one the database was set up with; refusing to host")

var repoRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,100}$`)

// New opens the storage on a database and a blob store, checking that the blob
// store is the one the database was set up with (and recording it the first
// time), and that the cache directory is usable.
func New(ctx context.Context, o Options) (*Store, error) {
	if o.DB == nil || o.Blobs == nil || o.CacheDir == "" {
		return nil, errors.New("repository storage needs a database, a blob store and a cache directory")
	}
	if o.Binary == "" {
		bin, err := os.Executable()
		if err != nil {
			return nil, err
		}
		o.Binary = bin
	}
	if o.WarnFraction <= 0 {
		o.WarnFraction = 0.8
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if err := os.MkdirAll(o.CacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("the repository cache directory: %w", err)
	}
	s := &Store{o: o, l: NewLedger(o.DB), lg: o.Log}
	if err := s.checkMarker(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Ledger is the database side, for callers that need the refs of record.
func (s *Store) Ledger() *Ledger { return s.l }

// markerPrefix is where the marker object lives in the blob store.
const markerPrefix = "launchway-repo-marker/"

// checkMarker is the discipline of the artifact store marker: a small object
// whose key the database holds. A server whose blob store lacks it (another
// directory, another bucket) refuses to host instead of keeping repositories
// where the other servers cannot find them.
func (s *Store) checkMarker(ctx context.Context) error {
	var key string
	err := s.o.DB.QueryRow(ctx, `SELECT value FROM meta WHERE key = 'repo_store_marker'`).Scan(&key)
	switch {
	case err == nil:
		if _, err := s.o.Blobs.Stat(ctx, key); err != nil {
			if errors.Is(err, blobstore.ErrNotFound) {
				return fmt.Errorf("%w (the marker %s is missing from the %s store)", ErrWrongStore, key, s.o.Blobs.Name())
			}
			return fmt.Errorf("checking the repository store marker: %w", err)
		}
		return nil
	case !isNoRows(err):
		return err
	}
	mine := markerPrefix + randomID()
	if err := putBytes(ctx, s.o.Blobs, mine, []byte("This object tells Launchway servers that they share this repository store. Keep it.\n")); err != nil {
		return fmt.Errorf("the repository store cannot be written: %w", err)
	}
	if _, err := s.o.DB.Exec(ctx, `INSERT INTO meta(key, value) VALUES('repo_store_marker', ?) ON CONFLICT(key) DO NOTHING`, mine); err != nil {
		return err
	}
	// Another server may have won: use the one that is recorded.
	return s.checkMarker(ctx)
}

// Create makes a repository exist and ready to be pushed to.
func (s *Store) Create(ctx context.Context, repo string) error {
	if !repoRE.MatchString(repo) {
		return fmt.Errorf("invalid repository id %q", repo)
	}
	return s.l.Create(ctx, repo)
}

// Refs returns the refs of record: what every server serves.
func (s *Store) Refs(ctx context.Context, repo string) (map[string]string, error) {
	snap, err := s.l.Snapshot(ctx, repo)
	return snap.Refs, err
}

// RPC is one request of git's stateless protocol: what the HTTP handler hands
// over.
type RPC struct {
	// Advertise asks for the ref advertisement (GET info/refs); In is unused.
	Advertise bool
	// In is the request body, already decompressed.
	In io.Reader
	// Out receives git's response.
	Out io.Writer
	// GitProtocol is the value of the Git-Protocol header, passed as GIT_PROTOCOL
	// so protocol version 2 works for fetch.
	GitProtocol string
}

func (r RPC) env() []string {
	if validProtocol.MatchString(r.GitProtocol) {
		return []string{"GIT_PROTOCOL=" + r.GitProtocol}
	}
	return nil
}

var validProtocol = regexp.MustCompile(`^[0-9a-zA-Z]+=[0-9a-zA-Z]+(:[0-9a-zA-Z]+=[0-9a-zA-Z]+)*$`)

func (s *Store) cacheDir(repo string) string { return filepath.Join(s.o.CacheDir, repo+".git") }

// Wipe deletes this server's cache of a repository, for tests and for the
// least-recently-used eviction. The next request rebuilds it.
func (s *Store) Wipe(repo string) error { return os.RemoveAll(s.cacheDir(repo)) }

func isNoRows(err error) bool { return err != nil && err.Error() == "sql: no rows in result set" }

func randomID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
