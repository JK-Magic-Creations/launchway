package storagespike

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fingerprintOf(t testing.TB, srv Server) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "fp.git")
	if err := srv.Clone(context.Background(), dest); err != nil {
		t.Fatalf("clone: %v", err)
	}
	fp, err := Fingerprint(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// Wiping a server's local state loses nothing for B (the cache is rebuilt
// from the database and the blob store); for A the volume is the data.
func TestFetchAfterCacheLoss(t *testing.T) {
	ctx := context.Background()
	t.Run("B", func(t *testing.T) {
		for _, kind := range []string{"B-fs", "B-db"} {
			e := newEnv(t, kind, false)
			s1, _ := e.sys.NewServer("s1")
			s2, _ := e.sys.NewServer("s2")
			for i := 0; i < 5; i++ {
				e.commitAndPush(t, s1, Fault{}, 30000)
			}
			before := fingerprintOf(t, s2)
			if err := e.sys.LoseNode(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(e.sys.(*LedgerSystem).Root, "servers", "s1", "repo.git")); err == nil {
				t.Fatal("the cache survived being wiped")
			}
			after := fingerprintOf(t, s1)
			if before != after {
				t.Errorf("%s: after the cache was wiped a fetch returned different content", kind)
			}
			// A push on a server whose cache was wiped works and loses nothing.
			if out, _ := e.commitAndPush(t, s2, Fault{}, 1000); !out.Acked {
				t.Errorf("%s: a push to a server that lost its cache: %s", kind, out.Output)
			}
			verifyProperties(t, e.sys, e.ack)
		}
	})
	t.Run("A", func(t *testing.T) {
		e := newEnv(t, "A", false)
		s1, _ := e.sys.NewServer("s1")
		if err := s1.WipeLocal(); err != ErrNoCache {
			t.Fatalf("A has no cache: %v", err)
		}
	})
}

// A killed sync (at each of its steps) leaves a cache the next sync repairs;
// refs never point at objects the cache lacks.
func TestKilledSyncLeavesARepairableCache(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "B-fs", false)
	s1, _ := e.sys.NewServer("s1")
	for i := 0; i < 3; i++ {
		e.commitAndPush(t, s1, Fault{}, 20000)
	}
	cold, _ := e.sys.NewServer("cold")
	cs := cold.(*ledgerServer)
	for _, pt := range []string{"sync:before-download", "sync:after-download", "sync:before-refs"} {
		os.RemoveAll(cs.cache)
		r, err := cs.Replica(Fault{Point: pt, Action: "error"})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Sync(ctx); err == nil {
			t.Fatalf("%s: the sync was not interrupted", pt)
		}
		r.Close()
		// Whatever it left: every ref present points at present objects.
		if refs, err := RefsOf(ctx, cs.cache); err == nil {
			for n, sha := range refs {
				if _, err := git(ctx, cs.cache, "", "cat-file", "-e", sha+"^{commit}"); err != nil {
					t.Errorf("%s: %s points at %s, which the interrupted cache does not have", pt, n, short(sha))
				}
			}
		}
		if _, err := cs.Sync(ctx, Fault{}); err != nil {
			t.Fatalf("%s: the next sync does not repair it: %v", pt, err)
		}
		if err := Fsck(ctx, cs.cache); err != nil {
			t.Errorf("%s: fsck after the repair: %v", pt, err)
		}
	}
	verifyProperties(t, e.sys, e.ack)
}

// B: consolidation (repack) retires packs; deleting the retired blobs, even
// with no grace period, never loses reachable objects; a replica caught
// mid-sync when its pack is retired and deleted reads the manifest again.
func TestMaintenanceNeverDeletesReachableObjects(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"B-fs", "B-db"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t, kind, false)
			s1, _ := e.sys.NewServer("s1")
			for i := 0; i < 6; i++ {
				e.commitAndPush(t, s1, Fault{}, 40000)
			}
			e.c.Branch(ctx, "feature", "HEAD~3")
			if out := e.push(t, s1, Fault{}); !out.Acked { // main at HEAD~3? keep refs interesting
				_ = out
			}
			before := fingerprintOf(t, s1)
			snap, _ := e.sys.(*LedgerSystem).snapshot(ctx)
			if len(snap.Packs) < 6 {
				t.Fatalf("expected a pack per push, have %d", len(snap.Packs))
			}
			if err := e.sys.Maintain(ctx); err != nil {
				t.Fatal(err)
			}
			snap2, _ := e.sys.(*LedgerSystem).snapshot(ctx)
			t.Logf("packs live before maintenance %d, after %d", len(snap.Packs), len(snap2.Packs))
			if len(snap2.Packs) != 1 {
				t.Errorf("expected one consolidated pack, have %d", len(snap2.Packs))
			}
			if after := fingerprintOf(t, s1); after != before {
				t.Error("maintenance changed what a fetch returns")
			}
			e.sys.LoseNode(ctx)
			if after := fingerprintOf(t, s1); after != before {
				t.Error("after maintenance and cache loss a fetch returns different content")
			}
			verifyProperties(t, e.sys, e.ack)
		})
	}
	t.Run("a replica mid-sync when its pack is retired and deleted", func(t *testing.T) {
		e := newEnv(t, "B-fs", false)
		s1, _ := e.sys.NewServer("s1")
		for i := 0; i < 4; i++ {
			e.commitAndPush(t, s1, Fault{}, 20000)
		}
		cold, _ := e.sys.NewServer("cold")
		r, err := cold.(*ledgerServer).Replica(Fault{})
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		once := false
		r.BeforeDownload = func(PackInfo) {
			if once {
				return
			}
			once = true
			// Another server consolidates and deletes the old blobs, grace 0,
			// after this one read the manifest.
			if err := e.sys.Maintain(ctx); err != nil {
				t.Error(err)
			}
		}
		if err := r.Sync(ctx); err != nil {
			t.Fatalf("the sync did not recover: %v", err)
		}
		if r.LastSync.Restarts < 1 {
			t.Errorf("expected the sync to read the manifest again, restarts=%d", r.LastSync.Restarts)
		}
		if err := Fsck(ctx, cold.(*ledgerServer).cache); err != nil {
			t.Error(err)
		}
		verifyProperties(t, e.sys, e.ack)
	})
	t.Run("pushes that never committed are collected, committed ones are not", func(t *testing.T) {
		e := newEnv(t, "B-fs", false)
		s1, _ := e.sys.NewServer("s1")
		e.commitAndPush(t, s1, Fault{}, 20000)
		for _, pt := range []string{PtAfterRegister, PtTxAfterRefs} {
			// The push dies; the client then abandons that commit and pushes
			// different work, so the dead push's pack is never committed.
			base, _ := e.c.Head(ctx)
			e.commitAndPush(t, s1, Fault{Point: pt, Action: "kill"}, 20000)
			e.c.Reset(ctx, base)
			if out, _ := e.commitAndPush(t, s1, Fault{}, 20000); !out.Acked {
				t.Fatalf("%s", out.Output)
			}
		}
		r, _ := s1.(*ledgerServer).Replica(Fault{})
		defer r.Close()
		n, err := r.Collect(ctx, 24*time.Hour, 0)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Errorf("expected the 2 packs of the dead pushes to be collected, collected %d", n)
		}
		t.Logf("collected %d blobs of pushes that never committed", n)
		e.sys.LoseNode(ctx)
		verifyProperties(t, e.sys, e.ack)
	})
}

// A: git's own gc, run over and over while pushes are in flight, never
// deletes an acknowledged push or leaves a ref at missing objects. With the
// default prune grace, in-flight quarantine objects survive.
func TestGitGCDuringPushes(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "A", false)
	s1, _ := e.sys.NewServer("s1")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var gcs atomic.Int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := e.sys.Maintain(ctx); err == nil {
				gcs.Add(1)
			}
		}
	}()
	acked := 0
	for i := 0; i < 15; i++ {
		if out, _ := e.commitAndPush(t, s1, Fault{}, 30000); out.Acked {
			acked++
		} else {
			e.converge(t, s1)
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("%d pushes acknowledged while git gc ran %d times", acked, gcs.Load())
	verifyProperties(t, e.sys, e.ack)
}

// Node loss. B: servers' disks are all wiped; nothing acknowledged is lost
// because the data is in the database and the blob store. A: the volume is
// deleted; the off-node mirror is whatever the last push-mirror run copied,
// so acknowledged pushes after it are gone: that is what a volume costs, and
// the number of pushes lost is the measure of the mirror's lag.
func TestNodeLoss(t *testing.T) {
	ctx := context.Background()
	t.Run("B", func(t *testing.T) {
		e := newEnv(t, "B-fs", false)
		s1, _ := e.sys.NewServer("s1")
		for i := 0; i < 8; i++ {
			e.commitAndPush(t, s1, Fault{}, 10000)
		}
		if err := e.sys.LoseNode(ctx); err != nil {
			t.Fatal(err)
		}
		verifyProperties(t, e.sys, e.ack)
	})
	t.Run("A with a mirror run after every third push", func(t *testing.T) {
		e := newEnv(t, "A", false)
		vs := e.sys.(*VolumeSystem)
		s1, _ := e.sys.NewServer("s1")
		for i := 1; i <= 8; i++ {
			e.commitAndPush(t, s1, Fault{}, 10000)
			if i%3 == 0 {
				if err := vs.Mirror(ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := e.sys.LoseNode(ctx); err != nil {
			t.Fatal(err)
		}
		auth, _ := e.sys.Authoritative(ctx)
		head, _ := e.c.Head(ctx)
		cur := auth["refs/heads/main"]
		lost := 0
		if cur != head {
			n, _ := e.c.git(ctx, "rev-list", "--count", cur+".."+head)
			fmt.Sscanf(n, "%d", &lost)
			if cur == "" {
				lost = 8
			}
		}
		t.Logf("A: after losing the volume, %d of 8 acknowledged pushes are gone (the mirror ran after pushes 3 and 6)", lost)
		if lost != 2 {
			t.Errorf("expected exactly the 2 pushes after the last mirror run to be lost, lost %d", lost)
		}
		// What is left is consistent.
		if cur != "" {
			if err := Fsck(ctx, vs.Repo()); err != nil {
				t.Error(err)
			}
		}
	})
}

// Backup and restore, byte-identical: B (database plus blob store) and A (a
// copy of the volume, taken while the repository is quiet).
func TestBackupAndRestoreAreByteIdentical(t *testing.T) {
	ctx := context.Background()
	t.Run("B", func(t *testing.T) {
		for _, kind := range []string{"B-fs", "B-db"} {
			e := newEnv(t, kind, false)
			s1, _ := e.sys.NewServer("s1")
			for i := 0; i < 5; i++ {
				e.commitAndPush(t, s1, Fault{}, 30000)
			}
			e.c.Branch(ctx, "other", "HEAD~2")
			e.push(t, s1, Fault{})
			before := fingerprintOf(t, s1)
			ls := e.sys.(*LedgerSystem)
			backup := filepath.Join(t.TempDir(), "backup")
			if err := ls.Backup(ctx, backup); err != nil {
				t.Fatal(err)
			}
			restored, err := RestoreLedgerSystem(ctx, backup, t.TempDir(), kind[2:], ls.RepoID)
			if err != nil {
				t.Fatal(err)
			}
			rs, _ := restored.NewServer("restored")
			if after := fingerprintOf(t, rs); after != before {
				t.Errorf("%s: a restored system is not byte-identical", kind)
			}
			verifyProperties(t, restored, e.ack)
		}
	})
	t.Run("A", func(t *testing.T) {
		e := newEnv(t, "A", false)
		s1, _ := e.sys.NewServer("s1")
		for i := 0; i < 5; i++ {
			e.commitAndPush(t, s1, Fault{}, 30000)
		}
		before := fingerprintOf(t, s1)
		vs := e.sys.(*VolumeSystem)
		copyDir := filepath.Join(t.TempDir(), "restored.git")
		if err := copyTree(vs.Repo(), copyDir); err != nil {
			t.Fatal(err)
		}
		fp, err := Fingerprint(ctx, copyDir)
		if err != nil || fp != before {
			t.Errorf("a copy of the volume differs: %v", err)
		}
		if err := Fsck(ctx, copyDir); err != nil {
			t.Error(err)
		}
	})
}
