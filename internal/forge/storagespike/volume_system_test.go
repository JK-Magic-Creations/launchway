package storagespike

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// VolumeSystem is candidate A: one bare repository on a persistent volume,
// written only by the real git binary through its own atomic ref
// transactions, with a ledger of ref updates in the database for events and
// audit, and off-node copies made by mirroring. Every server is a git process
// set on the same volume.
type VolumeSystem struct {
	Root   string
	DBAddr string
	RepoID string
	Binary string
	// Fsync is core.fsync ("" for git's default, "all" for the strongest).
	Fsync string

	mu        sync.Mutex
	repo      string // the volume's repository
	mirror    string // the off-node copy, refreshed by Mirror
	mirrorAt  time.Time
	initiated bool
}

// NewVolumeSystem makes the volume's bare repository and the ledger tables.
func NewVolumeSystem(ctx context.Context, root, dbAddr, repo, fsync string) (*VolumeSystem, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	s := &VolumeSystem{Root: root, DBAddr: dbAddr, RepoID: repo, Binary: bin, Fsync: fsync,
		repo: filepath.Join(root, "volume", "repo.git"), mirror: filepath.Join(root, "offnode", "repo.git")}
	cfg := Config{DBAddr: dbAddr}
	if err := InitSchema(ctx, cfg); err != nil {
		return nil, err
	}
	if err := s.initRepo(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *VolumeSystem) config(server string) Config {
	return Config{Mode: "volume", DBAddr: s.DBAddr, RepoID: s.RepoID, Cache: s.repo, Server: server, Binary: s.Binary, Fsync: s.Fsync}
}

func (s *VolumeSystem) initRepo(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.repo), 0o755); err != nil {
		return err
	}
	if _, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"init", "-q", "--bare", s.repo}}); err != nil {
		return err
	}
	for _, kv := range [][2]string{{"gc.auto", "0"}, {"receive.fsckObjects", "true"}, {"core.logAllRefUpdates", "false"}} {
		if _, err := git(ctx, s.repo, "", "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := git(ctx, s.repo, "", "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	hooks := filepath.Join(s.repo, "launchway", "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return err
	}
	for _, n := range hookNames {
		if err := os.Symlink(s.Binary, filepath.Join(hooks, n)); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

// Name is "A volume".
func (s *VolumeSystem) Name() string {
	if s.Fsync != "" {
		return "A-volume-fsync-" + s.Fsync
	}
	return "A-volume"
}

// NewServer adds a server: another git process set on the same volume.
func (s *VolumeSystem) NewServer(name string) (Server, error) {
	p, err := s.config(name).Write(filepath.Join(s.Root, "servers", name))
	if err != nil {
		return nil, err
	}
	return &volumeServer{sys: s, name: name, cfgPath: p}, nil
}

// Authoritative reads the refs from the volume.
func (s *VolumeSystem) Authoritative(ctx context.Context) (map[string]string, error) {
	return RefsOf(ctx, s.repo)
}

// Mirror refreshes the off-node copy: a push-mirror run. Whatever was pushed
// after the last Mirror is not in it.
func (s *VolumeSystem) Mirror(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initiated {
		if _, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"init", "-q", "--bare", s.mirror}}); err != nil {
			return err
		}
		s.initiated = true
	}
	_, err := gitexec.Run(ctx, gitexec.Spec{GitDir: s.repo, Args: []string{"push", "-q", "--mirror", "file://" + s.mirror}, Timeout: 30 * time.Minute})
	s.mirrorAt = time.Now()
	return err
}

// LoseNode deletes the volume and restarts on an empty one, restoring from
// the off-node mirror, if there is one. Pushes acknowledged after the last
// Mirror are gone.
func (s *VolumeSystem) LoseNode(ctx context.Context) error {
	if err := os.RemoveAll(filepath.Dir(s.repo)); err != nil {
		return err
	}
	if err := s.initRepo(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	have := s.initiated
	s.mu.Unlock()
	if !have {
		return nil
	}
	_, err := gitexec.Run(ctx, gitexec.Spec{GitDir: s.repo, Args: []string{"fetch", "-q", "file://" + s.mirror, "+refs/*:refs/*"}, Timeout: 30 * time.Minute})
	return err
}

// Maintain runs git's own gc (repack and prune, with git's default grace for
// unreachable objects).
func (s *VolumeSystem) Maintain(ctx context.Context) error {
	_, err := git(ctx, s.repo, "", "gc", "-q")
	return err
}

// Close does nothing.
func (s *VolumeSystem) Close() error { return nil }

// Repo is the volume's repository directory.
func (s *VolumeSystem) Repo() string { return s.repo }

// MirrorDir is the off-node copy.
func (s *VolumeSystem) MirrorDir() string { return s.mirror }

type volumeServer struct {
	sys     *VolumeSystem
	name    string
	cfgPath string
}

func (s *volumeServer) Name() string { return s.name }

func (s *volumeServer) Push(ctx context.Context, client string, f Fault, refspecs ...string) (PushOutcome, error) {
	wrapper := filepath.Join(s.sys.repo, "launchway", "hooks", "receive-pack-wrapper")
	return runPush(ctx, s.cfgPath, wrapper, s.sys.repo, client, f, refspecs)
}

func (s *volumeServer) Clone(ctx context.Context, dest string) error {
	return cloneMirror(ctx, s.sys.repo, dest)
}

func (s *volumeServer) WipeLocal() error { return ErrNoCache }

var _ = fmt.Sprintf
