package storagespike

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LedgerSystem is candidate B: refs and a pack manifest in the database, packs
// in the blob store, a disposable cache on each server.
type LedgerSystem struct {
	Root     string
	DBAddr   string
	BlobKind string // "fs" or "db"
	RepoID   string
	Binary   string // the executable the hook symlinks point at
	// VerifyUpload reads each pack back and checks its SHA-256 before the push
	// can commit.
	VerifyUpload bool

	mu      sync.Mutex
	servers []*ledgerServer
}

// NewLedgerSystem sets up the shared database tables and blob store under
// root. dbAddr is a SQLite path or a PostgreSQL URL.
func NewLedgerSystem(ctx context.Context, root, dbAddr, blobKind, repo string) (*LedgerSystem, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	s := &LedgerSystem{Root: root, DBAddr: dbAddr, BlobKind: blobKind, RepoID: repo, Binary: bin}
	cfg := s.config("setup", "")
	cfg.BlobDir = filepath.Join(root, "blobs")
	if err := InitSchema(ctx, cfg); err != nil {
		return nil, err
	}
	l, err := OpenLedger(dbAddr)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	if err := l.EnsureRepo(ctx, repo); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *LedgerSystem) config(server, cache string) Config {
	return Config{Mode: "ledger", DBAddr: s.DBAddr, BlobKind: s.BlobKind, BlobDir: filepath.Join(s.Root, "blobs"), RepoID: s.RepoID,
		Cache: cache, Server: server, VerifyUpload: s.VerifyUpload, Binary: s.Binary}
}

// Name is "B ledger (<blob store>)".
func (s *LedgerSystem) Name() string { return "B-ledger-" + s.BlobKind }

// NewServer adds a server with an empty cache.
func (s *LedgerSystem) NewServer(name string) (Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	srv := &ledgerServer{sys: s, name: name, cache: filepath.Join(s.Root, "servers", name, "repo.git")}
	srv.cfgPath, _ = srv.cfg().Write(filepath.Join(s.Root, "servers", name, "cfg"))
	s.servers = append(s.servers, srv)
	return srv, nil
}

// Authoritative reads the refs from the database.
func (s *LedgerSystem) Authoritative(ctx context.Context) (map[string]string, error) {
	l, err := OpenLedger(s.DBAddr)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	snap, err := l.Snapshot(ctx, s.RepoID)
	return snap.Refs, err
}

// LoseNode wipes every server's cache: all the local state there is. Nothing
// else is touched: the database and the blob store are elsewhere.
func (s *LedgerSystem) LoseNode(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, srv := range s.servers {
		if err := srv.WipeLocal(); err != nil {
			return err
		}
	}
	return nil
}

// Maintain repacks and collects with no grace, the harshest setting.
func (s *LedgerSystem) Maintain(ctx context.Context) error {
	srv, err := s.NewServer(fmt.Sprintf("maint-%d", time.Now().UnixNano()))
	if err != nil {
		return err
	}
	r, err := srv.(*ledgerServer).open(Fault{})
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Maintain(ctx); err != nil {
		return err
	}
	_, err = r.Collect(ctx, 0, 24*time.Hour)
	return err
}

// Close does nothing: connections are opened per use.
func (s *LedgerSystem) Close() error { return nil }

type ledgerServer struct {
	sys     *LedgerSystem
	name    string
	cache   string
	cfgPath string
}

func (s *ledgerServer) cfg() Config { return s.sys.config(s.name, s.cache) }

func (s *ledgerServer) Name() string { return s.name }

func (s *ledgerServer) open(f Fault) (*Replica, error) { return OpenReplica(s.cfg(), f) }

func (s *ledgerServer) Push(ctx context.Context, client string, f Fault, refspecs ...string) (PushOutcome, error) {
	r, err := s.open(Fault{})
	if err != nil {
		return PushOutcome{}, err
	}
	err = r.InitCache(ctx)
	r.Close()
	if err != nil {
		return PushOutcome{}, err
	}
	wrapper := filepath.Join(s.cache, "launchway", "hooks", "receive-pack-wrapper")
	return runPush(ctx, s.cfgPath, wrapper, s.cache, client, f, refspecs)
}

func (s *ledgerServer) Clone(ctx context.Context, dest string) error {
	r, err := s.open(Fault{})
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Sync(ctx); err != nil {
		return err
	}
	return cloneMirror(ctx, s.cache, dest)
}

func (s *ledgerServer) WipeLocal() error { return os.RemoveAll(filepath.Dir(s.cache) + "/repo.git") }

// Sync makes this server current and returns what the sync did.
func (s *ledgerServer) Sync(ctx context.Context, f Fault) (SyncStats, error) {
	r, err := s.open(f)
	if err != nil {
		return SyncStats{}, err
	}
	defer r.Close()
	err = r.Sync(ctx)
	return r.LastSync, err
}

// Replica opens the underlying replica (for tests that need its hooks).
func (s *ledgerServer) Replica(f Fault) (*Replica, error) { return s.open(f) }

// snapshot reads the manifest and refs from the database.
func (s *LedgerSystem) snapshot(ctx context.Context) (Snapshot, error) {
	l, err := OpenLedger(s.DBAddr)
	if err != nil {
		return Snapshot{}, err
	}
	defer l.Close()
	return l.Snapshot(ctx, s.RepoID)
}

// Backup copies the database (SQLite: a consistent copy with VACUUM INTO) and
// the blob store, which is what one backup of candidate B is. With the
// database blob store the one file is the whole backup.
func (s *LedgerSystem) Backup(ctx context.Context, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	l, err := OpenLedger(s.DBAddr)
	if err != nil {
		return err
	}
	defer l.Close()
	if l.PG {
		return fmt.Errorf("the PostgreSQL backup is pg_dump's job, outside this spike")
	}
	if _, err := l.DB().ExecContext(ctx, "VACUUM INTO '"+filepath.Join(dest, "ledger.db")+"'"); err != nil {
		return err
	}
	if s.BlobKind == "fs" {
		return copyTree(filepath.Join(s.Root, "blobs"), filepath.Join(dest, "blobs"))
	}
	return nil
}

// RestoreLedgerSystem brings up candidate B from a backup, on empty disks.
func RestoreLedgerSystem(ctx context.Context, backup, root, blobKind, repo string) (*LedgerSystem, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	db := filepath.Join(root, "ledger.db")
	if err := copyTree(filepath.Join(backup, "ledger.db"), db); err != nil {
		return nil, err
	}
	if blobKind == "fs" {
		if err := copyTree(filepath.Join(backup, "blobs"), filepath.Join(root, "blobs")); err != nil {
			return nil, err
		}
	}
	return &LedgerSystem{Root: root, DBAddr: db, BlobKind: blobKind, RepoID: repo, Binary: bin}, nil
}
