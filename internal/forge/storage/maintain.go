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

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Maintenance defaults.
const (
	// RetiredGrace is how long a retired pack's blob is kept: a server that
	// read the manifest just before the retirement still finds what it is
	// downloading.
	RetiredGrace = 24 * time.Hour
	// PendingAge is how long a pending pack is kept: an upload whose push never
	// committed.
	PendingAge = 24 * time.Hour
	// ConsolidateAbove is the number of live packs above which maintenance
	// consolidates.
	ConsolidateAbove = 8
)

// Maintain consolidates a repository's packs under the maintenance lease: it
// repacks everything reachable from the refs, as of one generation, into one
// pack, uploads it, and in one transaction makes it live and retires the packs
// it replaces. The caches are never repacked or tidied in place: they are
// rebuilt from the manifest. It reports whether it consolidated. minPacks is
// the number of live packs from which it is worth doing (ConsolidateAbove when
// 0; 2 forces it).
func (s *Store) Maintain(ctx context.Context, repo string, minPacks int) (bool, error) {
	if minPacks <= 0 {
		minPacks = ConsolidateAbove
	}
	ok, err := s.l.Lease(ctx, repo, s.o.Server, 10*time.Minute)
	if err != nil || !ok {
		return false, err
	}
	snap, err := s.l.Snapshot(ctx, repo)
	if err != nil {
		return false, err
	}
	if snap.Flagged != "" {
		return false, fmt.Errorf("the repository is flagged (%s); not repacking it", snap.Flagged)
	}
	if len(snap.Packs) < minPacks {
		return false, nil
	}
	if err := s.ensureCurrent(ctx, repo); err != nil {
		return false, err
	}
	dir := s.cacheDir(repo)
	// Repack into a scratch file: the cache's own packs stay as the manifest
	// describes them.
	scratch, err := os.MkdirTemp(s.o.CacheDir, "repack-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(scratch)
	var tips strings.Builder
	names := make([]string, 0, len(snap.Refs))
	for n := range snap.Refs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&tips, "%s\n", snap.Refs[n])
	}
	packPath := filepath.Join(scratch, "pack-new.pack")
	f, err := os.Create(packPath)
	if err != nil {
		return false, err
	}
	_, err = gitexec.Run(ctx, gitexec.Spec{GitDir: dir, Args: []string{"pack-objects", "--revs", "--stdout", "-q"},
		Stdin: strings.NewReader(tips.String()), Stdout: f, IdleTimeout: 10 * time.Minute, Limits: s.o.Limits})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, fmt.Errorf("repacking %s: %w", repo, err)
	}
	// The index gives the object count and lets the pack be checked.
	if _, err := s.git(ctx, dir, "", "index-pack", packPath); err != nil {
		return false, err
	}
	np, err := putPack(ctx, s.o.Blobs, repo, packPath, !s.o.NoVerifyUpload)
	if err != nil {
		return false, err
	}
	if err := s.l.RegisterPack(ctx, repo, np); err != nil {
		return false, err
	}
	if err := s.l.Consolidate(ctx, repo, snap.Gen, np); err != nil {
		return false, err
	}
	return true, nil
}

// Collect deletes the blobs of packs retired more than grace ago, and of
// pending packs older than pendingAge (uploads whose push never committed). It
// returns how many blobs it deleted. A zero grace is allowed, for tests of the
// harshest setting; production uses RetiredGrace.
func (s *Store) Collect(ctx context.Context, repo string, grace, pendingAge time.Duration) (int, error) {
	n := 0
	retired, err := s.l.Retired(ctx, repo, nowMS()-grace.Milliseconds())
	if err != nil {
		return n, err
	}
	for _, p := range retired {
		if err := s.o.Blobs.Delete(ctx, p.BlobKey); err != nil {
			return n, err
		}
		if err := s.l.SetPackState(ctx, repo, p.ID, PackRetired, PackOrphan); err != nil {
			return n, err
		}
		n++
	}
	pend, err := s.l.Pending(ctx, repo, nowMS()-pendingAge.Milliseconds())
	if err != nil {
		return n, err
	}
	for _, p := range pend {
		// A pending pack may be the very pack a push in flight is about to make
		// live; the age keeps this from ever being a fresh one.
		if err := s.o.Blobs.Delete(ctx, p.BlobKey); err != nil {
			return n, err
		}
		if err := s.l.SetPackState(ctx, repo, p.ID, PackPending, PackOrphan); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// VerifyReport is what Verify found.
type VerifyReport struct {
	PacksChecked   int
	CacheRebuilt   bool
	Problems       []string
	RepoFlagged    bool
	BytesRechecked int64
}

// Verify is the periodic integrity job: it re-reads up to sample live packs
// from the blob store and checks each against the SHA-256 in the manifest, and
// checks this server's cache with git fsck, rebuilding it if it is damaged. A
// pack that does not match flags the repository, which stops maintenance from
// repacking it and shows in Usage.
func (s *Store) Verify(ctx context.Context, repo string, sample int) (VerifyReport, error) {
	var rep VerifyReport
	snap, err := s.l.Snapshot(ctx, repo)
	if err != nil {
		return rep, err
	}
	packs := snap.Packs
	if sample > 0 && len(packs) > sample {
		// A rotating sample: the packs checked longest ago are not tracked in
		// this spike-sized job, so take the oldest first.
		packs = packs[:sample]
	}
	for _, p := range packs {
		sum, err := hashBlob(ctx, s.o.Blobs, p.BlobKey)
		rep.PacksChecked++
		rep.BytesRechecked += p.Bytes
		switch {
		case errors.Is(err, blobstore.ErrNotFound):
			rep.Problems = append(rep.Problems, fmt.Sprintf("pack %s is missing from the blob store", short(p.ID)))
		case err != nil:
			return rep, err
		case sum != p.ID:
			rep.Problems = append(rep.Problems, fmt.Sprintf("pack %s does not match its SHA-256 (read back %s)", short(p.ID), short(sum)))
		}
	}
	if len(rep.Problems) > 0 {
		rep.RepoFlagged = true
		if err := s.l.Flag(ctx, repo, strings.Join(rep.Problems, "; ")); err != nil {
			return rep, err
		}
	}
	if _, err := os.Stat(filepath.Join(s.cacheDir(repo), "HEAD")); err == nil {
		if _, err := s.git(ctx, s.cacheDir(repo), "", "fsck", "--full", "--no-dangling"); err != nil {
			rep.Problems = append(rep.Problems, "this server's cache is damaged and was rebuilt: "+err.Error())
			if err := s.Rebuild(ctx, repo); err != nil {
				return rep, err
			}
			rep.CacheRebuilt = true
		}
	}
	return rep, nil
}

// Warning is one thing a person should know about a repository's storage.
type Warning struct{ Message string }

// UsageReport is what the storage page shows for one repository.
type UsageReport struct {
	Usage
	Flagged  string
	Warnings []string
}

// Usage reports a repository's storage, with warnings well before a cap or a
// ceiling of the blob store is reached.
func (s *Store) Usage(ctx context.Context, repo string) (UsageReport, error) {
	snap, err := s.l.Snapshot(ctx, repo)
	if err != nil {
		return UsageReport{}, err
	}
	u, err := s.l.Usage(ctx, repo)
	if err != nil {
		return UsageReport{}, err
	}
	rep := UsageReport{Usage: u, Flagged: snap.Flagged}
	if snap.Flagged != "" {
		rep.Warnings = append(rep.Warnings, "the repository is flagged as damaged: "+snap.Flagged)
	}
	if c := s.o.MaxRepoBytes; c > 0 && float64(u.LiveBytes) >= s.o.WarnFraction*float64(c) {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("the repository takes %s of the %s one repository may take", mib(u.LiveBytes), mib(c)))
	}
	if u.LivePacks > ConsolidateAbove {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d packs are waiting to be consolidated", u.LivePacks))
	}
	if c, ok := s.o.Blobs.(interface{ Limits() (int64, int64) }); ok {
		maxTotal, maxObject := c.Limits()
		if maxObject > 0 && float64(u.LiveBytes) >= s.o.WarnFraction*float64(maxObject) {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("the repository (%s) is nearing the database store's largest object (%s); use an S3 or filesystem store for repositories this large", mib(u.LiveBytes), mib(maxObject)))
		}
		if maxTotal > 0 && float64(u.LiveBytes) >= s.o.WarnFraction*float64(maxTotal) {
			rep.Warnings = append(rep.Warnings, "the repository is nearing the whole capacity of the database store")
		}
	}
	return rep, nil
}

// EvictCaches deletes least-recently-used caches until this server's cache
// directory is at most maxBytes, never one that is in use (a cache whose
// guard cannot be taken exclusively is left alone). It returns how many it
// removed.
func (s *Store) EvictCaches(maxBytes int64) (int, error) {
	ents, err := os.ReadDir(s.o.CacheDir)
	if err != nil {
		return 0, err
	}
	type cache struct {
		name string
		size int64
		used time.Time
	}
	var all []cache
	var total int64
	for _, e := range ents {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".git") {
			continue
		}
		dir := filepath.Join(s.o.CacheDir, e.Name())
		c := cache{name: e.Name()}
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				c.size += info.Size()
			}
			return nil
		})
		if st, err := os.Stat(filepath.Join(dir, "launchway", "gen")); err == nil {
			c.used = st.ModTime()
		}
		total += c.size
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].used.Before(all[j].used) })
	removed := 0
	for _, c := range all {
		if total <= maxBytes {
			break
		}
		g, err := openGuard(filepath.Join(s.o.CacheDir, c.name))
		if err != nil {
			continue
		}
		if g.tryExclusive() {
			if os.RemoveAll(filepath.Join(s.o.CacheDir, c.name)) == nil {
				total -= c.size
				removed++
			}
		}
		g.release()
	}
	return removed, nil
}
