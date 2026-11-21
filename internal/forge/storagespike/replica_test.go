package storagespike

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

// hookNames are the hooks (and the wrapper) the binary answers to when it is
// started under one of these names: symlinks to one executable, no shell and
// no script in the repository (doc 26 section 4.3).
var hookNames = []string{"pre-receive", "reference-transaction", "post-receive", "receive-pack-wrapper"}

// git runs git in a repository with a generous timeout.
func git(ctx context.Context, gitDir string, stdin string, args ...string) (string, error) {
	spec := gitexec.Spec{GitDir: gitDir, Args: args, Timeout: 10 * time.Minute}
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

// Replica is one server of candidate B: a disposable cache of the repository
// next to the shared database and blob store.
type Replica struct {
	Cfg   Config
	L     *Ledger
	Blob  blobstore.Store
	Fault Fault
	// BeforeDownload, when set, runs before each pack download (to let a test
	// retire a pack under a replica that is mid-sync).
	BeforeDownload func(PackInfo)
	// Stats of the last sync.
	LastSync SyncStats
}

// SyncStats says what a sync did.
type SyncStats struct {
	Full       bool
	Packs      int
	Bytes      int64
	Restarts   int
	Generation int64
}

// OpenReplica connects a server to the shared database and blob store.
func OpenReplica(cfg Config, f Fault) (*Replica, error) {
	l, err := OpenLedger(cfg.DBAddr)
	if err != nil {
		return nil, err
	}
	l.Fault = f
	st, err := cfg.OpenBlob(l, f)
	if err != nil {
		l.Close()
		return nil, err
	}
	return &Replica{Cfg: cfg, L: l, Blob: st, Fault: f}, nil
}

// Close releases the connections.
func (r *Replica) Close() error { return r.L.Close() }

func (r *Replica) meta() string { return filepath.Join(r.Cfg.Cache, "launchway") }

// InitCache makes the bare repository, its hooks and its configuration, if
// they are not there. Hooks are symlinks to the one executable.
func (r *Replica) InitCache(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(r.Cfg.Cache, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.Cfg.Cache), 0o755); err != nil {
		return err
	}
	if _, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"init", "-q", "--bare", r.Cfg.Cache}}); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{"gc.auto", "0"}, {"receive.autogc", "false"}, {"receive.unpackLimit", "1"}, {"transfer.unpackLimit", "1"},
		{"receive.fsckObjects", "true"}, {"core.logAllRefUpdates", "false"}, {"uploadpack.allowAnySHA1InWant", "false"},
		{"core.hooksPath", filepath.Join(r.meta(), "hooks")},
	} {
		if _, err := git(ctx, r.Cfg.Cache, "", "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	if _, err := git(ctx, r.Cfg.Cache, "", "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	hooks := filepath.Join(r.meta(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return err
	}
	for _, n := range hookNames {
		if err := os.Symlink(r.Cfg.Binary, filepath.Join(hooks, n)); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(r.meta(), "packs"), 0o755)
}

func (r *Replica) readGen() int64 {
	data, err := os.ReadFile(filepath.Join(r.meta(), "gen"))
	if err != nil {
		return -1
	}
	var n int64
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &n)
	return n
}

func (r *Replica) writeGen(g int64) error {
	p := filepath.Join(r.meta(), "gen")
	if err := os.WriteFile(p+".tmp", []byte(fmt.Sprint(g)), 0o644); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// guard is the per-repository lock of one server's copy, taken with flock so
// that the kernel releases it when a process dies, however it dies. Running
// receive-packs hold it shared; sync and stale-lock cleanup hold it exclusive,
// which is how a lock file left by a killed git can be told from one in use:
// when nobody holds the guard shared, no receive-pack is running, and every
// "*.lock" file in the repository is garbage.
type guard struct{ f *os.File }

func openGuard(dir string) (*guard, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
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
func (g *guard) release()      { syscall.Flock(int(g.f.Fd()), syscall.LOCK_UN); g.f.Close() }

// CleanStale removes what a killed git leaves behind: ref and config lock
// files, and the quarantine directories of pushes that never finished. The
// caller holds the guard exclusively, so nothing here is in use.
func CleanStale(repo string) (removed int) {
	rm := func(p string) {
		if os.RemoveAll(p) == nil {
			removed++
		}
	}
	filepath.WalkDir(repo, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repo, p)
		switch {
		case d.IsDir() && strings.HasPrefix(d.Name(), "tmp_objdir-"):
			rm(p)
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(d.Name(), ".lock") && !strings.HasPrefix(rel, "launchway"):
			rm(p)
		case !d.IsDir() && strings.HasPrefix(rel, filepath.Join("objects", "pack")+string(filepath.Separator)+"tmp_"):
			rm(p)
		case !d.IsDir() && strings.HasPrefix(d.Name(), "push-") && strings.HasSuffix(d.Name(), ".json"):
			rm(p)
		}
		return nil
	})
	return removed
}

// Sync brings the cache to the database's state: it reads the repository's
// generation (one indexed read) and, if the cache is behind, downloads the
// packs it lacks, indexes them, and writes the refs, in that order, so a
// ref never points at objects the cache does not have. A pack that was
// retired under it makes it read the manifest again.
func (r *Replica) Sync(ctx context.Context) error {
	g, err := openGuard(r.meta())
	if err != nil {
		return err
	}
	defer g.release()
	if err := g.exclusive(); err != nil {
		return err
	}
	return r.syncLocked(ctx)
}

// syncLocked is Sync with the guard already held exclusively.
func (r *Replica) syncLocked(ctx context.Context) error {
	if err := r.InitCache(ctx); err != nil {
		return err
	}
	CleanStale(r.Cfg.Cache)
	r.LastSync = SyncStats{}
	for attempt := 0; attempt < 5; attempt++ {
		snap, err := r.L.Snapshot(ctx, r.Cfg.RepoID)
		if err != nil {
			return err
		}
		if snap.Gen == r.readGen() {
			r.LastSync.Generation = snap.Gen
			return nil
		}
		err = r.apply(ctx, snap)
		if errors.Is(err, blobstore.ErrNotFound) {
			r.LastSync.Restarts++ // retired while we were downloading: read the manifest again
			continue
		}
		if err != nil {
			return err
		}
		r.LastSync.Generation = snap.Gen
		return nil
	}
	return errors.New("the manifest kept changing under the sync")
}

func (r *Replica) apply(ctx context.Context, snap Snapshot) error {
	if err := CheckMarker(ctx, r.L, r.Blob); err != nil {
		return err
	}
	if r.readGen() < 0 {
		r.LastSync.Full = true
	}
	for _, p := range snap.Packs {
		marker := filepath.Join(r.meta(), "packs", p.ID)
		if _, err := os.Stat(marker); err == nil {
			continue
		}
		if r.BeforeDownload != nil {
			r.BeforeDownload(p)
		}
		if err := r.Fault.Hit("sync:before-download"); err != nil {
			return err
		}
		packDir := filepath.Join(r.Cfg.Cache, "objects", "pack")
		tmp := filepath.Join(packDir, "tmp_"+p.ID+".pack")
		if err := GetPack(ctx, r.Blob, p, tmp); err != nil {
			return err
		}
		if _, err := git(ctx, r.Cfg.Cache, "", "index-pack", tmp); err != nil {
			os.Remove(tmp)
			return err
		}
		final := filepath.Join(packDir, "pack-"+p.ID)
		if err := os.Rename(tmp, final+".pack"); err != nil {
			return err
		}
		if err := os.Rename(strings.TrimSuffix(tmp, ".pack")+".idx", final+".idx"); err != nil {
			return err
		}
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			return err
		}
		r.LastSync.Packs++
		r.LastSync.Bytes += p.Bytes
		if err := r.Fault.Hit("sync:after-download"); err != nil {
			return err
		}
	}
	// Refs last: every object they name is now in the cache.
	out, err := git(ctx, r.Cfg.Cache, "", "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return err
	}
	have := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if n, t, ok := strings.Cut(line, " "); ok {
			have[n] = t
		}
	}
	var cmds strings.Builder
	names := make([]string, 0, len(snap.Refs))
	for n := range snap.Refs {
		names = append(names, n)
	}
	sort.Strings(names)
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
	if err := r.Fault.Hit("sync:before-refs"); err != nil {
		return err
	}
	if cmds.Len() > 0 {
		if _, err := git(ctx, r.Cfg.Cache, cmds.String(), "update-ref", "--stdin"); err != nil {
			return err
		}
	}
	return r.writeGen(snap.Gen)
}

// WipeCache deletes everything this server holds locally. The next request
// rebuilds it from the database and the blob store.
func (r *Replica) WipeCache() error { return os.RemoveAll(r.Cfg.Cache) }

// Refs returns the refs the cache holds right now (after a sync, the refs of
// the database).
func (r *Replica) Refs(ctx context.Context) (map[string]string, error) {
	out, err := git(ctx, r.Cfg.Cache, "", "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if n, t, ok := strings.Cut(line, " "); ok {
			m[n] = t
		}
	}
	return m, nil
}

// Maintain consolidates the repository's packs: under the maintenance lease
// it repacks everything reachable from the refs (as of one snapshot) into
// one pack, uploads it, and in one transaction makes it live and retires the
// packs it replaces. Retired blobs are deleted only by Collect, after a grace
// period, so a slow replica mid-sync never finds a hole it cannot recover
// from.
func (r *Replica) Maintain(ctx context.Context) (bool, error) {
	ok, err := r.L.Lease(ctx, r.Cfg.RepoID, r.Cfg.Server, 10*time.Minute)
	if err != nil || !ok {
		return false, err
	}
	if err := r.Sync(ctx); err != nil {
		return false, err
	}
	g, err := openGuard(r.meta())
	if err != nil {
		return false, err
	}
	defer g.release()
	if err := g.exclusive(); err != nil {
		return false, err
	}
	snap, err := r.L.Snapshot(ctx, r.Cfg.RepoID)
	if err != nil {
		return false, err
	}
	if snap.Gen != r.readGen() || len(snap.Packs) < 2 {
		return false, nil // someone pushed since the sync, or nothing to consolidate
	}
	// Repack into a scratch repository so the cache's own packs stay as the
	// manifest describes them.
	scratch, err := os.MkdirTemp(filepath.Dir(r.Cfg.Cache), "repack-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(scratch)
	if _, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"init", "-q", "--bare", scratch}}); err != nil {
		return false, err
	}
	var want strings.Builder
	for n, t := range snap.Refs {
		fmt.Fprintf(&want, "%s\n", t)
		_ = n
	}
	// pack-objects of everything reachable from the refs, written as one pack.
	spec := gitexec.Spec{GitDir: r.Cfg.Cache, Args: []string{"pack-objects", "--revs", "--stdout", "-q"},
		Stdin: strings.NewReader(want.String()), Timeout: 30 * time.Minute, MaxOutput: 1 << 40}
	packFile := filepath.Join(scratch, "new.pack")
	f, err := os.Create(packFile)
	if err != nil {
		return false, err
	}
	spec.Stdout = f
	_, err = gitexec.Run(ctx, spec)
	f.Close()
	if err != nil {
		return false, fmt.Errorf("repacking: %w", err)
	}
	np, err := PutPack(ctx, r.Blob, r.Cfg.RepoID, packFile, r.Cfg.VerifyUpload)
	if err != nil {
		return false, err
	}
	if err := r.L.RegisterPack(ctx, r.Cfg.RepoID, np); err != nil {
		return false, err
	}
	if err := r.Fault.Hit("maintain:before-consolidate"); err != nil {
		return false, err
	}
	if err := r.L.Consolidate(ctx, r.Cfg.RepoID, snap.Gen, snap.Refs, np); err != nil {
		return false, err
	}
	return true, nil
}

// Collect deletes the blobs of packs retired more than grace ago, and
// orphans pending packs (uploads whose push never committed) older than
// pendingAge. It returns how many blobs it deleted.
func (r *Replica) Collect(ctx context.Context, grace, pendingAge time.Duration) (int, error) {
	n := 0
	retired, err := r.L.Retired(ctx, r.Cfg.RepoID, nowMS()-grace.Milliseconds())
	if err != nil {
		return n, err
	}
	for _, p := range retired {
		if err := r.Blob.Delete(ctx, p.BlobKey); err != nil {
			return n, err
		}
		if err := r.L.MarkDeleted(ctx, r.Cfg.RepoID, p.ID); err != nil {
			return n, err
		}
		n++
	}
	pend, err := r.L.Pending(ctx, r.Cfg.RepoID, nowMS()-pendingAge.Milliseconds())
	if err != nil {
		return n, err
	}
	for _, p := range pend {
		if err := r.Blob.Delete(ctx, p.BlobKey); err != nil {
			return n, err
		}
		if err := r.L.SetPackState(ctx, r.Cfg.RepoID, p.ID, "pending", "orphan"); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
