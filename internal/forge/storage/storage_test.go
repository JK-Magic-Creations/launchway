package storage

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

var blobKinds = []string{"fs", "db"}

// converge retries a push with no fault until it is acknowledged: after any
// failure a client's retry succeeds.
func converge(t testing.TB, c *client, p *proc, repo string, ack *[]ackedPush) {
	t.Helper()
	for i := 0; i < 4; i++ {
		head := c.head()
		if r := c.push(p, repo, testFault{}, "main:refs/heads/main"); r.Acked {
			*ack = append(*ack, ackedPush{"refs/heads/main", head})
			return
		} else if i == 3 {
			t.Fatalf("a retry after the failure was still refused: %s", r.Output)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func pushOK(t testing.TB, c *client, p *proc, repo string, ack *[]ackedPush, size int) string {
	t.Helper()
	sha := c.commit(size)
	r := c.push(p, repo, testFault{}, "main:refs/heads/main")
	if !r.Acked {
		t.Fatalf("push: %s", r.Output)
	}
	*ack = append(*ack, ackedPush{"refs/heads/main", sha})
	return sha
}

// A push and a fetch over real smart HTTP, protocol v2, shallow and partial
// clones, and a fetch on another server right after the push.
func TestPushAndFetchAcrossServers(t *testing.T) {
	for _, kind := range blobKinds {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, kind, false)
			a, b := w.server("a"), w.server("b")
			c := newClient(t)
			var ack []ackedPush
			for i := 0; i < 4; i++ {
				srv, other := a, b
				if i%2 == 1 {
					srv, other = b, a
				}
				sha := pushOK(t, c, srv, w.repo, &ack, 20000)
				dest := cloneMirror(t, other, w.repo) // read-your-writes: the other server sees it at once
				if refsOf(t, dest)["refs/heads/main"] != sha {
					t.Fatalf("round %d: a fetch on the other server right after the push did not see it", i)
				}
			}
			shallow := filepath.Join(t.TempDir(), "shallow")
			if _, err := gitexec.Run(context.Background(), gitexec.Spec{Args: []string{"clone", "-q", "--depth=1", a.repoURL(w.repo), shallow}, Timeout: time.Minute}); err != nil {
				t.Errorf("shallow clone: %v", err)
			}
			partial := filepath.Join(t.TempDir(), "partial")
			if _, err := gitexec.Run(context.Background(), gitexec.Spec{Args: []string{"clone", "-q", "--filter=blob:none", "--no-checkout", a.repoURL(w.repo), partial}, Timeout: time.Minute}); err != nil {
				t.Errorf("partial clone: %v", err)
			}
			w.verify(t, ack)
		})
	}
}

// Kill -9 of the server at every step of a push: the retry succeeds, nothing
// acknowledged is lost, no ref points at missing objects.
func TestKillTheServerAtEveryStepOfAPush(t *testing.T) {
	for _, kind := range blobKinds {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, kind, false)
			srv := w.server("a")
			c := newClient(t)
			var ack []ackedPush
			pushOK(t, c, srv, w.repo, &ack, 8000)
			for _, pt := range PushPoints {
				c.commit(8000)
				r := c.push(srv, w.repo, testFault{Point: pt, Action: "kill"}, "main:refs/heads/main")
				if r.Acked {
					t.Fatalf("%s: the push was acknowledged although the server died", pt)
				}
				srv = srv.Restart()
				converge(t, c, srv, w.repo, &ack)
				w.verify(t, ack)
				t.Logf("%-28s kill -9 of the server: retry converged, properties hold", pt)
			}
		})
	}
}

// kill -9 of git itself, at the points where it holds ref locks, leaves lock
// files behind; the next push removes them and goes through.
func TestKillingGitLeavesNoStuckLocks(t *testing.T) {
	w := newWorld(t, "fs", false)
	srv := w.server("a")
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, srv, w.repo, &ack, 8000)
	for _, pt := range []string{PtPrepared, PtTxAfterRefs, PtCommitted} {
		c.commit(8000)
		r := c.push(srv, w.repo, testFault{Point: pt, Action: "killgit"}, "main:refs/heads/main")
		t.Logf("%s: git killed, push acked=%v", pt, r.Acked)
		converge(t, c, srv, w.repo, &ack)
	}
	locks, _ := filepath.Glob(filepath.Join(w.root, "servers", "a", w.repo+".git", "refs", "heads", "*.lock"))
	if len(locks) != 0 {
		t.Errorf("stale locks left: %v", locks)
	}
	w.verify(t, ack)
}

// The database drops the connection in the middle of the commit, before and
// after it, and the in-transaction steps fail.
func TestDatabaseConnectionDrop(t *testing.T) {
	w := newWorld(t, "fs", false)
	srv := w.server("a")
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, srv, w.repo, &ack, 4000)
	for _, f := range []testFault{{Point: "db", Action: "dropbefore"}, {Point: "db", Action: "dropafter"},
		{Point: PtTxAfterRefs, Action: "error"}, {Point: PtTxAfterPack, Action: "error"}, {Point: PtTxBeforeCommit, Action: "error"}} {
		sha := c.commit(4000)
		r := c.push(srv, w.repo, f, "main:refs/heads/main")
		auth, _ := w.st.Refs(context.Background(), w.repo)
		committed := auth["refs/heads/main"] == sha
		if r.Acked {
			t.Fatalf("%s: acknowledged", f)
		}
		if f.Action == "dropbefore" || f.Point != "db" {
			if committed {
				t.Errorf("%s: the refs of record moved though the commit never happened", f)
			}
		} else if !committed {
			t.Errorf("%s: the commit happened (the answer was lost) but the refs of record did not move", f)
		}
		t.Logf("%-22s push failed; committed=%v", f, committed)
		converge(t, c, srv, w.repo, &ack)
		w.verify(t, ack)
	}
}

// The blob store fails, is slow, or lies.
func TestBlobStoreFaults(t *testing.T) {
	for _, tc := range []struct {
		f       testFault
		noVerif bool
		ack     bool
		why     string
	}{
		{testFault{Point: "blob", Action: "down"}, false, false, "blob store down: fails before the commit"},
		{testFault{Point: "upload:part", Action: "error"}, false, false, "an upload part fails"},
		{testFault{Point: "upload:data", Action: "truncate"}, false, false, "a pack stored cut short: the size check refuses"},
		{testFault{Point: "upload:data", Action: "flip"}, false, false, "a byte of the stored pack is wrong, same size: read-back refuses"},
		{testFault{Point: "blob", Action: "slow", Arg: "100ms"}, false, true, "a slow store: slower, not wrong"},
	} {
		t.Run(tc.f.String(), func(t *testing.T) {
			w := newWorld(t, "fs", false)
			srv := w.server("a", func(e *serverEnv) { e.NoVerify = tc.noVerif })
			c := newClient(t)
			var ack []ackedPush
			pushOK(t, c, srv, w.repo, &ack, 4000)
			sha := c.commit(30000)
			r := c.push(srv, w.repo, tc.f, "main:refs/heads/main")
			if r.Acked != tc.ack {
				t.Fatalf("acked=%v, want %v (%s): %s", r.Acked, tc.ack, tc.why, r.Output)
			}
			auth, _ := w.st.Refs(context.Background(), w.repo)
			if !r.Acked && auth["refs/heads/main"] == sha {
				t.Error("the push failed but the refs of record moved")
			}
			if r.Acked {
				ack = append(ack, ackedPush{"refs/heads/main", sha})
			}
			converge(t, c, srv, w.repo, &ack)
			w.verify(t, ack)
		})
	}
}

// A damaged pack is refused and the repository flagged: a cold server does not
// serve wrong data.
func TestDamagedPackIsRefusedAndTheRepositoryFlagged(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	srv := w.server("a")
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, srv, w.repo, &ack, 30000)
	// Damage the stored pack in place, as a failing disk would.
	var damaged int
	filepath.WalkDir(strings.TrimPrefix(w.blobs, "fs:"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".pack") || strings.Contains(p, "/packs/") && !d.IsDir() {
			data, _ := os.ReadFile(p)
			if len(data) > 100 {
				data[len(data)/2] ^= 0xff
				os.WriteFile(p, data, 0o644)
				damaged++
			}
		}
		return nil
	})
	if damaged == 0 {
		t.Fatal("found no pack to damage")
	}
	rep, err := w.st.Verify(ctx, w.repo, 0)
	if err != nil || !rep.RepoFlagged || len(rep.Problems) == 0 {
		t.Fatalf("Verify did not notice: %+v %v", rep, err)
	}
	snap, _ := w.st.Ledger().Snapshot(ctx, w.repo)
	if snap.Flagged == "" {
		t.Error("the repository is not flagged")
	}
	u, _ := w.st.Usage(ctx, w.repo)
	if len(u.Warnings) == 0 {
		t.Error("the damage is not in the warnings")
	}
	cold := w.server("cold")
	res, err := gitexec.Run(ctx, gitexec.Spec{Args: []string{"clone", "-q", "--mirror", cold.repoURL(w.repo), filepath.Join(t.TempDir(), "x.git")}, Timeout: time.Minute})
	if err == nil {
		t.Fatalf("a cold server served a damaged repository: %s", res.Stderr)
	}
	if _, err := w.st.Maintain(ctx, w.repo, 2); err == nil || !strings.Contains(err.Error(), "flagged") {
		t.Errorf("maintenance must not repack a flagged repository: %v", err)
	}
}

// Two servers push the same branch at once from the same base: one wins, one
// is refused as stale, the refs of record hold the winner. PostgreSQL: 100
// rounds.
func TestConcurrentPushesToOneRefSerialize(t *testing.T) {
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
			if !testdb.Postgres() {
				continue
			}
		}
		t.Run(name, func(t *testing.T) {
			rounds := envInt("FORGE_STORAGE_RACE_ROUNDS", 10)
			if pg {
				rounds = envInt("FORGE_STORAGE_RACE_ROUNDS", 100)
			}
			ctx := context.Background()
			w := newWorld(t, "fs", pg)
			a, b := w.server("a"), w.server("b")
			c1, c2 := newClient(t), newClient(t)
			c2.seed = 1_000_000 // the two clients must not write the same files
			var ack []ackedPush
			base := pushOK(t, c1, a, w.repo, &ack, 4000)
			c2.git("fetch", "-q", a.repoURL(w.repo), "main")
			c2.git("reset", "-q", "--hard", base)
			var wins1, wins2 int
			for i := 0; i < rounds; i++ {
				s1 := c1.commit(2000)
				s2 := c2.commit(2000)
				var r1, r2 pushResult
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); r1 = c1.push(a, w.repo, testFault{}, "main:refs/heads/main") }()
				go func() { defer wg.Done(); r2 = c2.push(b, w.repo, testFault{}, "main:refs/heads/main") }()
				wg.Wait()
				if r1.Acked == r2.Acked {
					t.Fatalf("round %d: acked %v and %v; exactly one must win\n1: %s\n2: %s", i, r1.Acked, r2.Acked, r1.Output, r2.Output)
				}
				auth, _ := w.st.Refs(ctx, w.repo)
				winner, loser, wc, lc := s1, s2, c1, c2
				if r2.Acked {
					winner, loser, wc, lc = s2, s1, c2, c1
					wins2++
				} else {
					wins1++
				}
				_ = loser
				if auth["refs/heads/main"] != winner {
					t.Fatalf("round %d: refs of record %s, winner %s", i, short(auth["refs/heads/main"]), short(winner))
				}
				ack = append(ack, ackedPush{"refs/heads/main", winner})
				// The loser takes the winner's commit; both start the next round from it.
				srv := a
				if wc == c2 {
					srv = b
				}
				lc.git("fetch", "-q", srv.repoURL(w.repo), "main")
				lc.git("reset", "-q", "--hard", winner)
			}
			t.Logf("%d rounds on %s: server a won %d, server b won %d, the other was refused every time", rounds, name, wins1, wins2)
			w.verify(t, ack)
		})
	}
}

// Wiping a server's cache loses nothing.
func TestWipingTheCacheLosesNothing(t *testing.T) {
	for _, kind := range blobKinds {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, kind, false)
			a := w.server("a")
			c := newClient(t)
			var ack []ackedPush
			for i := 0; i < 5; i++ {
				pushOK(t, c, a, w.repo, &ack, 30000)
			}
			before := fingerprint(t, cloneMirror(t, a, w.repo))
			a.Kill()
			if err := os.RemoveAll(filepath.Join(w.root, "servers", "a")); err != nil {
				t.Fatal(err)
			}
			a = a.Restart()
			if after := fingerprint(t, cloneMirror(t, a, w.repo)); after != before {
				t.Error("a fetch after the cache was wiped returned different content")
			}
			pushOK(t, c, a, w.repo, &ack, 1000)
			w.verify(t, ack)
		})
	}
}

// A cache that git finds damaged is rebuilt.
func TestDamagedCacheIsRebuilt(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	a := w.server("a")
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, a, w.repo, &ack, 30000)
	if err := w.st.ensureCurrent(ctx, w.repo); err != nil {
		t.Fatal(err)
	}
	packs, _ := filepath.Glob(filepath.Join(w.st.cacheDir(w.repo), "objects", "pack", "pack-*.pack"))
	if len(packs) == 0 {
		t.Fatal("no pack in the cache")
	}
	data, _ := os.ReadFile(packs[0])
	data[len(data)/2] ^= 0xff
	os.WriteFile(packs[0], data, 0o644)
	rep, err := w.st.Verify(ctx, w.repo, 0)
	if err != nil || !rep.CacheRebuilt {
		t.Fatalf("the damaged cache was not rebuilt: %+v %v", rep, err)
	}
	if rep.RepoFlagged {
		t.Error("a damaged cache is not a damaged repository")
	}
	w.verify(t, ack)
}

// Consolidation retires packs; collecting with no grace period never deletes
// reachable objects; a server whose pack was retired and deleted while it was
// downloading reads the manifest again.
func TestMaintenanceNeverDeletesReachableObjects(t *testing.T) {
	ctx := context.Background()
	for _, kind := range blobKinds {
		t.Run(kind, func(t *testing.T) {
			w := newWorld(t, kind, false)
			a := w.server("a")
			c := newClient(t)
			var ack []ackedPush
			for i := 0; i < 6; i++ {
				pushOK(t, c, a, w.repo, &ack, 40000)
			}
			before := fingerprint(t, cloneMirror(t, a, w.repo))
			if ok, err := w.st.Maintain(ctx, w.repo, 100); ok || err != nil {
				t.Fatalf("maintenance below its threshold: %v %v", ok, err)
			}
			ok, err := w.st.Maintain(ctx, w.repo, 2)
			if err != nil || !ok {
				t.Fatalf("consolidation: %v %v", ok, err)
			}
			n, err := w.st.Collect(ctx, w.repo, 0, 24*time.Hour)
			if err != nil || n != 6 {
				t.Fatalf("collected %d blobs, want the 6 retired: %v", n, err)
			}
			u, _ := w.st.Ledger().Usage(ctx, w.repo)
			if u.LivePacks != 1 {
				t.Errorf("live packs after consolidation: %d", u.LivePacks)
			}
			a.Kill()
			os.RemoveAll(filepath.Join(w.root, "servers", "a"))
			a = a.Restart()
			if after := fingerprint(t, cloneMirror(t, a, w.repo)); after != before {
				t.Error("after consolidation, deleting the retired blobs and losing the cache, a fetch returns different content")
			}
			pushOK(t, c, a, w.repo, &ack, 1000) // a push after consolidation
			w.verify(t, ack)
		})
	}
	t.Run("a server mid-sync when its pack is retired and deleted", func(t *testing.T) {
		w := newWorld(t, "fs", false)
		a := w.server("a")
		c := newClient(t)
		var ack []ackedPush
		for i := 0; i < 4; i++ {
			pushOK(t, c, a, w.repo, &ack, 20000)
		}
		e := serverEnv{DB: w.db, Blobs: w.blobs, Cache: filepath.Join(w.root, "racer"), Name: "racer"}
		racer, eng, err := openStore(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		once := false
		racer.beforeDownload = func(Pack) {
			if once {
				return
			}
			once = true
			if ok, err := w.st.Maintain(ctx, w.repo, 2); !ok || err != nil {
				t.Errorf("consolidation: %v %v", ok, err)
			}
			if _, err := w.st.Collect(ctx, w.repo, 0, 24*time.Hour); err != nil {
				t.Error(err)
			}
		}
		if err := racer.ensureCurrent(ctx, w.repo); err != nil {
			t.Fatalf("the sync did not recover: %v", err)
		}
		if !once {
			t.Fatal("the race never happened")
		}
		if _, err := racer.git(ctx, racer.cacheDir(w.repo), "", "fsck", "--full", "--no-dangling"); err != nil {
			t.Error(err)
		}
		w.verify(t, ack)
	})
	t.Run("uploads of pushes that never committed are collected", func(t *testing.T) {
		w := newWorld(t, "fs", false)
		a := w.server("a")
		c := newClient(t)
		var ack []ackedPush
		pushOK(t, c, a, w.repo, &ack, 20000)
		for _, pt := range []string{PtAfterRegister, PtTxAfterRefs} {
			base := c.head()
			c.commit(20000)
			c.push(a, w.repo, testFault{Point: pt, Action: "kill"}, "main:refs/heads/main")
			a = a.Restart()
			c.git("reset", "-q", "--hard", base) // abandon that work: its pack is never committed
			pushOK(t, c, a, w.repo, &ack, 20000)
		}
		n, err := w.st.Collect(ctx, w.repo, RetiredGrace, 0)
		if err != nil || n != 2 {
			t.Fatalf("collected %d, want the 2 dead uploads: %v", n, err)
		}
		w.verify(t, ack)
	})
}

// A server whose blob store is not the one the database was set up with
// refuses to host.
func TestForeignBlobStoreIsRefused(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	other, err := blobstore.NewFilesystem(filepath.Join(t.TempDir(), "elsewhere"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(ctx, Options{DB: w.eng.Database(), Blobs: other, CacheDir: filepath.Join(t.TempDir(), "c"), Server: "rogue"})
	if !errors.Is(err, ErrWrongStore) {
		t.Fatalf("a foreign store was accepted: %v", err)
	}
}

// Quotas: a push over the cap is refused with words, usage warns before the cap.
func TestQuotas(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	a := w.server("a", func(e *serverEnv) { e.MaxRepo = 300 << 10; e.MaxPack = 200 << 10 })
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, a, w.repo, &ack, 100<<10)
	c.commit(250 << 10)
	r := c.push(a, w.repo, testFault{}, "main:refs/heads/main")
	if r.Acked || !strings.Contains(r.Output, "one push may be") {
		t.Fatalf("a push over the pack limit: acked=%v %s", r.Acked, r.Output)
	}
	c.git("reset", "-q", "--hard", "HEAD~1")
	pushOK(t, c, a, w.repo, &ack, 150<<10)
	c.commit(150 << 10)
	r = c.push(a, w.repo, testFault{}, "main:refs/heads/main")
	if r.Acked || !strings.Contains(r.Output, "one repository may be") {
		t.Fatalf("a push over the repository cap: acked=%v %s", r.Acked, r.Output)
	}
	w.st.o.MaxRepoBytes = 300 << 10
	u, err := w.st.Usage(ctx, w.repo)
	if err != nil || len(u.Warnings) == 0 || !strings.Contains(u.Warnings[0], "may take") {
		t.Fatalf("no warning near the cap: %+v %v", u, err)
	}
	w.verify(t, ack)
}

// The cache directory is evicted least recently used, leaving caches in use.
func TestEvictionLeavesCachesInUse(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	c := newClient(t)
	a := w.server("a")
	for _, id := range []string{"rep2", "rep3"} {
		if err := w.st.Create(ctx, id); err != nil {
			t.Fatal(err)
		}
		sha := c.commit(40000)
		if r := c.push(a, id, testFault{}, "main:refs/heads/main"); !r.Acked {
			t.Fatal(r.Output)
		}
		_ = sha
	}
	for _, id := range []string{"rep2", "rep3"} {
		if err := w.st.ensureCurrent(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	inUse, err := w.st.guardFor("rep3")
	if err != nil {
		t.Fatal(err)
	}
	if err := inUse.shared(); err != nil {
		t.Fatal(err)
	}
	n, err := w.st.EvictCaches(1)
	if err != nil || n != 1 {
		t.Fatalf("evicted %d: %v", n, err)
	}
	inUse.release()
	if _, err := os.Stat(w.st.cacheDir("rep3")); err != nil {
		t.Error("a cache in use was evicted")
	}
	if _, err := os.Stat(w.st.cacheDir("rep2")); err == nil {
		t.Error("the cache not in use was kept")
	}
	// An evicted cache is rebuilt on the next request.
	if err := w.st.ensureCurrent(ctx, "rep2"); err != nil {
		t.Fatal(err)
	}
}

// The restore drill: back up the database and the blob store, restore them on
// empty disks, and prove the repository clones and verifies, byte for byte.
func TestRestoreDrill(t *testing.T) {
	for _, kind := range blobKinds {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t, kind, false)
			a := w.server("a")
			c := newClient(t)
			var ack []ackedPush
			for i := 0; i < 5; i++ {
				pushOK(t, c, a, w.repo, &ack, 30000)
			}
			c.git("checkout", "-q", "-b", "other", "HEAD~2")
			if r := c.push(a, w.repo, testFault{}, "other:refs/heads/other"); !r.Acked {
				t.Fatal(r.Output)
			}
			before := fingerprint(t, cloneMirror(t, a, w.repo))
			// Back up: a consistent copy of the database, and the blob directory.
			bk := t.TempDir()
			if _, err := w.eng.Database().Exec(ctx, "VACUUM INTO '"+filepath.Join(bk, "launchway.db")+"'"); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(w.blobs, "fs:") {
				if err := copyTree(strings.TrimPrefix(w.blobs, "fs:"), filepath.Join(bk, "blobs")); err != nil {
					t.Fatal(err)
				}
			}
			// Restore on empty disks and start a server there.
			root := t.TempDir()
			e := serverEnv{DB: filepath.Join(bk, "launchway.db"), Blobs: "db", Cache: filepath.Join(root, "cache"), Name: "restored"}
			if strings.HasPrefix(w.blobs, "fs:") {
				e.Blobs = "fs:" + filepath.Join(bk, "blobs")
			}
			restored := startServer(t, e)
			if after := fingerprint(t, cloneMirror(t, restored, w.repo)); after != before {
				t.Error("the restored repository is not byte-identical")
			}
			dest := cloneMirror(t, restored, w.repo)
			if _, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dest, Args: []string{"fsck", "--full", "--strict", "--no-dangling"}, Timeout: time.Minute}); err != nil {
				t.Error(err)
			}
		})
	}
}

func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// The campaign: rounds of pushes, alternating between two servers, each with a
// random failure injected; after a failed push the client retries on either
// server. At the end every property is checked from a cold server.
func TestInjectionCampaign(t *testing.T) {
	rounds := envInt("FORGE_STORAGE_ROUNDS", 40)
	pool := []testFault{}
	for _, pt := range PushPoints {
		pool = append(pool, testFault{Point: pt, Action: "kill"}, testFault{Point: pt, Action: "error"})
	}
	pool = append(pool, testFault{Point: PtPrepared, Action: "killgit"}, testFault{Point: PtCommitted, Action: "killgit"},
		testFault{Point: "db", Action: "dropbefore"}, testFault{Point: "db", Action: "dropafter"},
		testFault{Point: "blob", Action: "down"}, testFault{Point: "upload:part", Action: "error"},
		testFault{Point: "upload:data", Action: "truncate"}, testFault{Point: "upload:data", Action: "flip"})
	w := newWorld(t, "fs", false)
	srv := []*proc{w.server("a"), w.server("b")}
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, srv[0], w.repo, &ack, 4000)
	rng := newRand(int64(envInt("FORGE_STORAGE_SEED", 7)))
	injected, restarts := 0, 0
	for i := 0; i < rounds; i++ {
		f := testFault{}
		if rng.Intn(10) > 0 {
			f = pool[rng.Intn(len(pool))]
			injected++
		}
		s := srv[i%2]
		sha := c.commit(1000 + rng.Intn(100000))
		r := c.push(s, w.repo, f, "main:refs/heads/main")
		if r.Acked {
			ack = append(ack, ackedPush{"refs/heads/main", sha})
		} else {
			if f.Action == "kill" {
				// The fault ends the server's process; the push can fail a moment before the
				// process is gone, so wait for it to be gone, not for the check to be lucky.
				select {
				case <-s.done:
				case <-time.After(10 * time.Second): // the fault was not reached; the server lives
				}
			}
			select {
			case <-s.done: // the server died: start it again
				srv[i%2] = s.Restart()
				restarts++
			default:
			}
			converge(t, c, srv[(i+rng.Intn(2))%2], w.repo, &ack)
		}
		if i%15 == 14 {
			w.verify(t, ack)
		}
	}
	w.verify(t, ack)
	t.Logf("%d rounds, %d with a failure injected, %d server restarts, %d pushes acknowledged: every property held", rounds, injected, restarts, len(ack))
}

// A push to a server that is dead fails, and fails promptly: nothing waits on
// a server that stopped answering.
func TestPushToADeadServerFailsPromptly(t *testing.T) {
	w := newWorld(t, "fs", false)
	srv := w.server("a")
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, srv, w.repo, &ack, 4000)
	srv.Kill()
	c.commit(4000)
	start := time.Now()
	r := c.push(srv, w.repo, testFault{}, "main:refs/heads/main")
	if r.Acked {
		t.Fatal("a push to a dead server was acknowledged")
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("the push took %v to fail", d)
	}
}

// A server that is alive but never answers (a handler stuck) is given up on by
// the client's idle limit: the push fails within a bound, not never.
func TestPushToAServerThatNeverAnswersFailsWithinABound(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // accepted, read nothing, say nothing
		}
	}()
	c := newClient(t)
	c.commit(1000)
	start := time.Now()
	r := c.push(&proc{URL: "http://" + ln.Addr().String()}, "rep1", testFault{}, "main:refs/heads/main")
	if r.Acked || time.Since(start) > 80*time.Second {
		t.Fatalf("acked=%v after %v", r.Acked, time.Since(start))
	}
}
