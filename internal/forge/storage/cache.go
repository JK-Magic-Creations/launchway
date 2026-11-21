package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// git runs git in a repository through the one hardened runner.
func (s *Store) git(ctx context.Context, gitDir, stdin string, args ...string) (string, error) {
	spec := gitexec.Spec{GitDir: gitDir, Args: args, Timeout: 15 * time.Minute, Limits: s.o.Limits}
	if stdin != "" {
		spec.Stdin = strings.NewReader(stdin)
	}
	res, err := gitexec.Run(ctx, spec)
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return res.Stdout, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(ee.Stderr+ee.Stdout))
		}
		return res.Stdout, fmt.Errorf("git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// gitWith runs git in a repository with extra environment, such as the
// directory of a push's objects in quarantine.
func (s *Store) gitWith(ctx context.Context, gitDir string, env []string, args ...string) (string, error) {
	res, err := gitexec.Run(ctx, gitexec.Spec{GitDir: gitDir, Args: args, Env: env, AllowAlternates: true, Timeout: 5 * time.Minute, Limits: s.o.Limits})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return res.Stdout, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(ee.Stderr+ee.Stdout))
		}
		return res.Stdout, fmt.Errorf("git %s: %w", args[0], err)
	}
	return res.Stdout, nil
}

// guard is the per-repository lock of one server's cache, an flock so that the
// kernel releases it when a process dies, however it dies. Running
// receive-packs hold it shared. Sync and the removal of stale lock files hold
// it exclusive: when nobody holds it shared, no receive-pack is running, and a
// "*.lock" file is garbage from a git that was killed.
type guard struct{ f *os.File }

func openGuard(cache string) (*guard, error) {
	dir := filepath.Join(cache, "launchway")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return &guard{f}, nil
}

func (g *guard) exclusive() error { return syscall.Flock(int(g.f.Fd()), syscall.LOCK_EX) }
func (g *guard) tryExclusive() bool {
	return syscall.Flock(int(g.f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}
func (g *guard) shared() error { return syscall.Flock(int(g.f.Fd()), syscall.LOCK_SH) }
func (g *guard) release()      { _ = syscall.Flock(int(g.f.Fd()), syscall.LOCK_UN); g.f.Close() }

// cleanStale removes what a killed git leaves behind: ref and config lock
// files and the quarantine directories of pushes that never finished. The
// caller holds the guard exclusively, so none of it is in use.
func cleanStale(repo string) {
	//nolint:gosec // every path walked is inside this server's own cache directory
	_ = filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repo, p)
		packTmp := filepath.Join("objects", "pack") + string(filepath.Separator) + "tmp_"
		switch {
		case rel == "launchway" || strings.HasPrefix(rel, "launchway"+string(filepath.Separator)):
			if d.IsDir() {
				return filepath.SkipDir
			}
		case d.IsDir() && strings.HasPrefix(d.Name(), "tmp_objdir-"):
			_ = os.RemoveAll(p)
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(d.Name(), ".lock"):
			_ = os.Remove(p)
		case !d.IsDir() && strings.HasPrefix(rel, packTmp):
			_ = os.Remove(p)
		}
		return nil
	})
}

func (s *Store) hooksDir(repo string) string {
	return filepath.Join(s.cacheDir(repo), "launchway", "hooks")
}

// initCache makes the bare repository of the cache, with its configuration and
// its hooks (symlinks to the one executable), if it is not there.
func (s *Store) initCache(ctx context.Context, repo string) error {
	dir := s.cacheDir(repo)
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return err
	}
	if _, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"init", "-q", "--bare", dir}, Limits: s.o.Limits}); err != nil {
		return fmt.Errorf("making the cache of %s: %w", repo, err)
	}
	for _, kv := range [][2]string{
		{"gc.auto", "0"}, {"receive.autogc", "false"}, {"receive.unpackLimit", "1"}, {"transfer.unpackLimit", "1"},
		{"receive.fsckObjects", "true"}, {"transfer.fsckObjects", "true"}, {"core.logAllRefUpdates", "false"},
		{"uploadpack.allowAnySHA1InWant", "false"}, {"uploadpack.allowFilter", "true"}, {"transfer.hideRefs", "refs/launchway"},
		{"receive.advertiseAtomic", "true"}, {"core.fsync", "all"},
	} {
		if _, err := s.git(ctx, dir, "", "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := s.git(ctx, dir, "", "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	if err := os.MkdirAll(s.hooksDir(repo), 0o750); err != nil {
		return err
	}
	for _, n := range hookNames {
		if err := os.Symlink(s.o.Binary, filepath.Join(s.hooksDir(repo), n)); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(dir, "launchway", "packs"), 0o750)
}

func (s *Store) readGen(repo string) int64 {
	data, err := os.ReadFile(filepath.Join(s.cacheDir(repo), "launchway", "gen"))
	if err != nil {
		return -1
	}
	var n int64
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &n); err != nil {
		return -1
	}
	return n
}

func (s *Store) writeGen(repo string, g int64) error {
	p := filepath.Join(s.cacheDir(repo), "launchway", "gen")
	if err := os.WriteFile(p+".tmp", []byte(fmt.Sprint(g)), 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// ensureCurrent makes this server's cache of the repository current. One
// indexed read decides: if the cache is at the repository's generation it is
// used as it is.
func (s *Store) ensureCurrent(ctx context.Context, repo string) error {
	gen, err := s.l.Generation(ctx, repo)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(s.cacheDir(repo), "HEAD")); err == nil && s.readGen(repo) == gen {
		return nil
	}
	g, err := s.guardFor(repo)
	if err != nil {
		return err
	}
	defer g.release()
	if err := g.exclusive(); err != nil {
		return err
	}
	return s.syncLocked(ctx, repo)
}

func (s *Store) guardFor(repo string) (*guard, error) {
	if err := os.MkdirAll(s.cacheDir(repo), 0o750); err != nil {
		return nil, err
	}
	return openGuard(s.cacheDir(repo))
}

// syncLocked brings the cache to the database's state, the guard held
// exclusively: it downloads the packs the cache lacks, indexes them, and only
// then writes the refs, so a ref never points at objects the cache does not
// have. A pack that was retired while it was being downloaded makes it read the
// manifest again; a pack that is wrong flags the repository.
func (s *Store) syncLocked(ctx context.Context, repo string) error {
	if err := s.initCache(ctx, repo); err != nil {
		return err
	}
	cleanStale(s.cacheDir(repo))
	for attempt := 0; attempt < 6; attempt++ {
		snap, err := s.l.Snapshot(ctx, repo)
		if err != nil {
			return err
		}
		if snap.Gen == s.readGen(repo) {
			return nil
		}
		err = s.apply(ctx, repo, snap)
		switch {
		case errors.Is(err, blobstore.ErrNotFound):
			continue // retired under us: read the manifest again
		case errors.Is(err, ErrCorrupt):
			_ = s.l.Flag(ctx, repo, "a pack in the blob store does not match its SHA-256: "+err.Error())
			return err
		case err != nil:
			return err
		}
		return nil
	}
	return errors.New("the repository's manifest kept changing under the sync")
}

func (s *Store) apply(ctx context.Context, repo string, snap Snapshot) error {
	dir := s.cacheDir(repo)
	packDir := filepath.Join(dir, "objects", "pack")
	for _, p := range snap.Packs {
		marker := filepath.Join(dir, "launchway", "packs", p.ID)
		if _, err := os.Stat(marker); err == nil {
			continue
		}
		if s.beforeDownload != nil {
			s.beforeDownload(p)
		}
		tmp := filepath.Join(packDir, "tmp_"+p.ID+".pack")
		if err := getPack(ctx, s.o.Blobs, p, tmp); err != nil {
			return err
		}
		if _, err := s.git(ctx, dir, "", "index-pack", tmp); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("indexing pack %s: %w", short(p.ID), err)
		}
		final := filepath.Join(packDir, "pack-"+p.ID)
		if err := os.Rename(tmp, final+".pack"); err != nil {
			return err
		}
		if err := os.Rename(strings.TrimSuffix(tmp, ".pack")+".idx", final+".idx"); err != nil {
			return err
		}
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			return err
		}
	}
	out, err := s.git(ctx, dir, "", "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return err
	}
	have := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if n, t, ok := strings.Cut(line, " "); ok {
			have[n] = t
		}
	}
	names := make([]string, 0, len(snap.Refs))
	for n := range snap.Refs {
		names = append(names, n)
	}
	sort.Strings(names)
	var cmds strings.Builder
	for _, n := range names {
		if have[n] != snap.Refs[n] {
			fmt.Fprintf(&cmds, "update %s %s\n", n, snap.Refs[n])
		}
	}
	for n := range have {
		if _, ok := snap.Refs[n]; !ok {
			fmt.Fprintf(&cmds, "delete %s\n", n)
		}
	}
	if cmds.Len() > 0 {
		if _, err := s.git(ctx, dir, cmds.String(), "update-ref", "--stdin"); err != nil {
			return err
		}
	}
	return s.writeGen(repo, snap.Gen)
}

// Rebuild throws the cache of a repository away and builds it again from the
// manifest: what to do with a cache that git finds damaged.
func (s *Store) Rebuild(ctx context.Context, repo string) error {
	if err := s.Wipe(repo); err != nil {
		return err
	}
	return s.ensureCurrent(ctx, repo)
}
