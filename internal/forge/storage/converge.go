package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// ErrEmptySource is returned by Converge when the source has none of the
// refs a repository is kept in step with while the repository has some: a
// source that suddenly lists nothing is more likely broken, or hostile, than
// really empty, and following it would delete every branch.
var ErrEmptySource = errors.New("the source lists no branches or tags at all, though this repository has some; refusing to delete them all")

// ConvergeOptions say what a repository is made to match.
type ConvergeOptions struct {
	// Prefixes are the ref namespaces that are kept equal to the source's
	// (such as "refs/heads/"); no other ref of the repository is touched.
	Prefixes []string
	// Prune deletes refs the source no longer has; without it they stay.
	Prune bool
	// AllowEmpty lets a source with no refs delete everything (a source that
	// really was emptied, once an administrator has said so).
	AllowEmpty bool
	// MaxRefs refuses a source with more refs (0: 100000).
	MaxRefs int
	// Fetch brings the source's refs into scratch, a bare repository whose
	// objects borrow from the repository's own, under the same names the
	// source gives them. It returns when the objects are there.
	Fetch func(ctx context.Context, scratch string) error
}

// ConvergeResult says what a Converge changed.
type ConvergeResult struct {
	Created, Updated, Deleted, Unchanged int
	// Refs is how many managed refs the repository has now.
	Refs int
	// PackBytes is the size of the new objects, 0 when there were none.
	PackBytes int64
	// Generation is the repository's generation after the change (unchanged
	// when nothing changed).
	Generation int64
}

// Changed reports whether any ref moved.
func (r ConvergeResult) Changed() bool { return r.Created+r.Updated+r.Deleted > 0 }

func hasPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Converge makes the managed refs of a repository equal to a source's, in
// one commit: the new objects are stored as one pack, then every ref moves
// (or is deleted) in the one database transaction a push uses, so readers see
// the repository before or after, never between. It is the write path of a
// mirror: it does not check who may write, so only a mirror's sync calls it.
// A ref that moved while the source was being fetched (a push to the
// repository, another server's sync) fails the call with *ErrStale and
// changes nothing; the caller tries again later.
func (s *Store) Converge(ctx context.Context, repo string, o ConvergeOptions) (ConvergeResult, error) {
	var res ConvergeResult
	if o.Fetch == nil || len(o.Prefixes) == 0 {
		return res, errors.New("converge needs a source to fetch and the refs to keep equal")
	}
	if o.MaxRefs <= 0 {
		o.MaxRefs = 100000
	}
	if err := s.ensureCurrent(ctx, repo); err != nil {
		return res, err
	}
	g, err := s.guardFor(repo)
	if err != nil {
		return res, err
	}
	defer g.release()
	if err := g.shared(); err != nil {
		return res, err
	}
	cache := s.cacheDir(repo)
	snap, err := s.l.Snapshot(ctx, repo)
	if err != nil {
		return res, err
	}
	have := map[string]string{}
	for n, t := range snap.Refs {
		if hasPrefix(n, o.Prefixes) {
			have[n] = t
		}
	}

	scratch, err := os.MkdirTemp(s.o.CacheDir, ".converge-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(scratch)
	if err := s.prepareScratch(ctx, scratch, cache, snap.Refs); err != nil {
		return res, err
	}
	if err := o.Fetch(ctx, scratch); err != nil {
		return res, err
	}
	want, err := s.readWanted(ctx, scratch, o.Prefixes)
	if err != nil {
		return res, err
	}
	if len(want) > o.MaxRefs {
		return res, fmt.Errorf("the source has %d branches, tags and notes, more than the %d a repository may mirror", len(want), o.MaxRefs)
	}
	if len(want) == 0 && len(have) > 0 && !o.AllowEmpty {
		return res, ErrEmptySource
	}

	ups := diffRefs(have, want, o.Prune, &res)
	res.Generation = snap.Gen
	if len(ups) == 0 {
		return res, nil
	}

	err = s.storeAndCommit(ctx, repo, scratch, snap.Refs, ups, &res)
	return res, err
}

// storeAndCommit stores the new objects of ups as one pack, installs it in
// the cache, and commits the ref changes; it fills res as it goes.
func (s *Store) storeAndCommit(ctx context.Context, repo, scratch string, current map[string]string, ups []RefUpdate, res *ConvergeResult) error {
	cache := s.cacheDir(repo)
	pack, err := s.buildPack(ctx, scratch, current, ups)
	if err != nil {
		return err
	}
	var packID string
	if pack != "" {
		// Moved into the cache on success; otherwise it is removed here.
		defer func() {
			_ = os.Remove(pack)
			_ = os.Remove(strings.TrimSuffix(pack, ".pack") + ".idx")
		}()
		if packID, err = s.installPack(ctx, repo, cache, pack, current, ups, res); err != nil {
			return err
		}
	}
	before := s.readGen(repo)
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	gen, err := s.l.Commit(cctx, repo, ups, packID, orNone(faultsFrom(ctx)))
	if err != nil {
		return err
	}
	res.Generation = gen
	// The cache is made to match what was committed, so the next request does
	// not download the pack that is already here.
	var cmds strings.Builder
	for _, u := range ups {
		if isZero(u.New) {
			fmt.Fprintf(&cmds, "delete %s\n", u.Name)
		} else {
			fmt.Fprintf(&cmds, "update %s %s\n", u.Name, u.New)
		}
	}
	if _, err := s.git(ctx, cache, cmds.String(), "update-ref", "--stdin"); err == nil && before >= 0 && gen == before+1 {
		if packID != "" {
			_ = os.WriteFile(filepath.Join(cache, "launchway", "packs", packID), nil, 0o600)
		}
		_ = s.writeGen(repo, gen)
	}
	return nil
}

// installPack checks the quota, stores and registers the pack, moves it into
// the cache and checks that the new tips are connected. It returns the pack's ID.
func (s *Store) installPack(ctx context.Context, repo, cache, pack string, current map[string]string, ups []RefUpdate, res *ConvergeResult) (string, error) {
	fi, err := os.Stat(pack)
	if err != nil {
		return "", err
	}
	res.PackBytes = fi.Size()
	if err := s.checkQuota(ctx, repo, fi.Size()); err != nil {
		return "", err
	}
	p, err := putPack(ctx, s.o.Blobs, repo, pack, !s.o.NoVerifyUpload)
	if err != nil {
		return "", fmt.Errorf("storing the pack: %w", err)
	}
	if err := s.l.RegisterPack(ctx, repo, p); err != nil {
		return "", fmt.Errorf("registering the pack: %w", err)
	}
	final := filepath.Join(cache, "objects", "pack", "pack-"+p.ID)
	if err := os.Rename(pack, final+".pack"); err != nil {
		return "", err
	}
	if err := os.Rename(strings.TrimSuffix(pack, ".pack")+".idx", final+".idx"); err != nil {
		return "", err
	}
	if err := s.connected(ctx, cache, current, ups); err != nil {
		return "", err
	}
	return p.ID, nil
}

// diffRefs lists the updates that make have equal want (deletions only when
// prune), and counts them into res.
func diffRefs(have, want map[string]string, prune bool, res *ConvergeResult) []RefUpdate {
	var ups []RefUpdate
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		switch cur, ok := have[n]; {
		case !ok:
			ups = append(ups, RefUpdate{Name: n, Old: "", New: want[n]})
			res.Created++
		case cur != want[n]:
			ups = append(ups, RefUpdate{Name: n, Old: cur, New: want[n]})
			res.Updated++
		default:
			res.Unchanged++
		}
	}
	res.Refs = len(want)
	if prune {
		var gone []string
		for n := range have {
			if _, ok := want[n]; !ok {
				gone = append(gone, n)
			}
		}
		sort.Strings(gone)
		for _, n := range gone {
			ups = append(ups, RefUpdate{Name: n, Old: have[n], New: ""})
			res.Deleted++
		}
		return ups
	}
	res.Refs = len(have)
	for n := range want {
		if _, ok := have[n]; !ok {
			res.Refs++
		}
	}
	return ups
}

// prepareScratch makes the bare repository the source is fetched into: its
// objects borrow from the cache, so the transfer sends only what the
// repository lacks, and the repository's refs are in a namespace of their
// own, for git to say what it has when the source asks.
func (s *Store) prepareScratch(ctx context.Context, scratch, cache string, refs map[string]string) error {
	if _, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"init", "-q", "--bare", scratch}, Limits: s.o.Limits}); err != nil {
		return fmt.Errorf("making the scratch repository: %w", err)
	}
	alt := filepath.Join(scratch, "objects", "info")
	if err := os.MkdirAll(alt, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(alt, "alternates"), []byte(filepath.Join(cache, "objects")+"\n"), 0o600); err != nil {
		return err
	}
	seen := map[string]bool{}
	var cmds strings.Builder
	for _, t := range refs {
		if !seen[t] {
			seen[t] = true
			fmt.Fprintf(&cmds, "create refs/launchway-have/%s %s\n", t, t)
		}
	}
	if cmds.Len() == 0 {
		return nil
	}
	_, err := s.scratchGit(ctx, scratch, cmds.String(), "update-ref", "--stdin")
	return err
}

func (s *Store) scratchGit(ctx context.Context, scratch, stdin string, args ...string) (string, error) {
	spec := gitexec.Spec{GitDir: scratch, Args: args, Timeout: 30 * time.Minute, Limits: s.o.Limits, AllowAlternates: true, Untrusted: true}
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

// readWanted reads the managed refs the fetch left in scratch.
func (s *Store) readWanted(ctx context.Context, scratch string, prefixes []string) (map[string]string, error) {
	out, err := s.scratchGit(ctx, scratch, "", "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return nil, err
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		n, t, ok := strings.Cut(line, " ")
		if ok && hasPrefix(n, prefixes) && gitexec.ValidRef(n) == nil {
			want[n] = t
		}
	}
	return want, nil
}

// buildPack packs the objects the new tips need that the repository does not
// have, as one pack in a file whose index is next to it, or returns "" when
// no object is new.
func (s *Store) buildPack(ctx context.Context, scratch string, current map[string]string, ups []RefUpdate) (string, error) {
	var in strings.Builder
	tips := map[string]bool{}
	for _, u := range ups {
		if !isZero(u.New) && !tips[u.New] {
			tips[u.New] = true
			in.WriteString(u.New + "\n")
		}
	}
	if len(tips) == 0 {
		return "", nil
	}
	neg := map[string]bool{}
	for _, t := range current {
		if !neg[t] {
			neg[t] = true
			in.WriteString("^" + t + "\n")
		}
	}
	dir, err := os.MkdirTemp(scratch, "pack-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "new.pack")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	_, err = gitexec.Run(ctx, gitexec.Spec{GitDir: scratch, Args: []string{"pack-objects", "--revs", "--stdout", "--delta-base-offset", "-q"},
		Stdin: strings.NewReader(in.String()), Stdout: f, Timeout: 2 * time.Hour, IdleTimeout: 10 * time.Minute, Limits: s.o.Limits, AllowAlternates: true})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("packing the new objects: %w", err)
	}
	if _, err := s.scratchGit(ctx, scratch, "", "index-pack", path); err != nil {
		return "", fmt.Errorf("the objects the source sent are not a sound set: %w", err)
	}
	if objectCount(strings.TrimSuffix(path, ".pack")+".idx") == 0 {
		return "", nil
	}
	// The pack must outlive the scratch directory's removal only until it is
	// moved into the cache; both are under the same filesystem.
	moved := filepath.Join(s.o.CacheDir, ".converge-pack-"+randomID()+".pack")
	if err := os.Rename(path, moved); err != nil {
		return "", err
	}
	if err := os.Rename(strings.TrimSuffix(path, ".pack")+".idx", strings.TrimSuffix(moved, ".pack")+".idx"); err != nil {
		return "", err
	}
	return moved, nil
}

// connected checks, in the cache that now holds the new pack, that every new
// tip reaches only objects the repository has.
func (s *Store) connected(ctx context.Context, cache string, current map[string]string, ups []RefUpdate) error {
	var in strings.Builder
	n := 0
	for _, u := range ups {
		if !isZero(u.New) {
			in.WriteString(u.New + "\n")
			n++
		}
	}
	if n == 0 {
		return nil
	}
	// What the repository already has is excluded with a ^ before each tip: the git that Launchway
	// supports at the least (2.38) does not read "--not" from its standard input.
	for _, t := range current {
		in.WriteString("^" + t + "\n")
	}
	if _, err := s.git(ctx, cache, in.String(), "rev-list", "--objects", "--quiet", "--stdin"); err != nil {
		return fmt.Errorf("the objects of the new refs are not all there: %w", err)
	}
	return nil
}

// checkQuota refuses a pack the repository may not take, with a plain message.
func (s *Store) checkQuota(ctx context.Context, repo string, size int64) error {
	o := s.o
	if o.MaxPackBytes > 0 && size > o.MaxPackBytes {
		return fmt.Errorf("the new objects are %s, more than the %s one update may be", mib(size), mib(o.MaxPackBytes))
	}
	if o.MaxRepoBytes > 0 {
		u, err := s.l.Usage(ctx, repo)
		if err != nil {
			return err
		}
		if u.LiveBytes+u.PendingBytes+size > o.MaxRepoBytes {
			return fmt.Errorf("this repository would be %s, more than the %s one repository may be", mib(u.LiveBytes+u.PendingBytes+size), mib(o.MaxRepoBytes))
		}
	}
	return nil
}

// WithCache runs fn with the path of this server's bare cache of the
// repository, made current and held against a concurrent resync, for
// operations that read the whole repository with git (a push mirror's push).
// fn must not write to the repository.
func (s *Store) WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error {
	if err := s.ensureCurrent(ctx, repo); err != nil {
		return err
	}
	g, err := s.guardFor(repo)
	if err != nil {
		return err
	}
	defer g.release()
	if err := g.shared(); err != nil {
		return err
	}
	return fn(s.cacheDir(repo))
}

// Generation is the repository's generation: it moves with every change.
func (s *Store) Generation(ctx context.Context, repo string) (int64, error) {
	return s.l.Generation(ctx, repo)
}
