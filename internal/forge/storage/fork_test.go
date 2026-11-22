package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// failAt is a fault that errors once, at one named point.
type failAt struct {
	point string
	mu    sync.Mutex
	done  bool
}

func (f *failAt) Hit(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p == f.point && !f.done {
		f.done = true
		return injected(p)
	}
	return nil
}
func (f *failAt) Drop(string) error { return nil }

func (w *world) newRepo(t *testing.T, id string) {
	t.Helper()
	if err := w.st.Create(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

func hasObject(t *testing.T, dir, sha string) bool {
	t.Helper()
	_, err := gitexec.Run(context.Background(), gitexec.Spec{GitDir: dir, Args: []string{"cat-file", "-e", sha}, Timeout: time.Minute})
	return err == nil
}

func blobExists(t *testing.T, w *world, repo string) []bool {
	t.Helper()
	snap, err := w.st.Ledger().Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	var out []bool
	for _, p := range snap.Packs {
		_, err := w.st.o.Blobs.Stat(context.Background(), p.BlobKey)
		out = append(out, err == nil)
	}
	return out
}

// A fork is the parent as it was: its branches and objects, and nothing pushed
// to the parent afterwards, however the fork's cache is rebuilt.
func TestForkSeesTheParentAtTheForkAndNeverWhatIsPushedAfter(t *testing.T) {
	for _, kind := range blobKinds {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t, kind, false)
			a := inproc(t, w.st)
			c := newClient(t)
			var ack []ackedPush
			pushOK(t, c, a, w.repo, &ack, 4000)
			atFork := pushOK(t, c, a, w.repo, &ack, 4000)
			w.newRepo(t, "frk1")
			res, err := w.st.Fork(ctx, w.repo, "frk1")
			if err != nil || res.Refs != 1 || res.Packs < 1 || res.Generation != 1 {
				t.Fatalf("fork: %+v %v", res, err)
			}
			// The secret is pushed to the parent after the fork.
			secret := pushOK(t, c, a, w.repo, &ack, 4000)
			for round, wipe := range []bool{false, true} {
				if wipe {
					if err := w.st.Wipe("frk1"); err != nil {
						t.Fatal(err)
					}
				}
				dest := cloneMirror(t, a, "frk1")
				if got := refsOf(t, dest); got["refs/heads/main"] != atFork || len(got) != 1 {
					t.Fatalf("round %d: the fork serves %v, want only main at %s", round, got, short(atFork))
				}
				if hasObject(t, dest, secret) {
					t.Fatalf("round %d: AN OBJECT PUSHED TO THE PARENT AFTER THE FORK IS IN THE FORK", round)
				}
				if _, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dest, Args: []string{"fsck", "--full", "--strict", "--no-dangling"}, Timeout: time.Minute}); err != nil {
					t.Fatalf("fsck of the fork: %v", err)
				}
			}
			// Asking the fork for the secret by name does not work either.
			stranger := newClient(t)
			if _, err := gitexec.Run(ctx, gitexec.Spec{Dir: stranger.dir, Args: []string{"fetch", a.repoURL("frk1"), secret}, Timeout: time.Minute}); err == nil {
				t.Error("the fork handed out an object of the parent that was pushed after the fork")
			}
			// Pushes to the fork do not reach the parent.
			fc := newClient(t)
			fc.git("fetch", "-q", a.repoURL("frk1"), "main")
			fc.git("reset", "-q", "--hard", "FETCH_HEAD")
			own := fc.commit(2000)
			if r := fc.push(a, "frk1", testFault{}, "main:refs/heads/main"); !r.Acked {
				t.Fatalf("push to the fork: %s", r.Output)
			}
			if refs, _ := w.st.Refs(ctx, w.repo); refs["refs/heads/main"] != secret {
				t.Errorf("the parent's main moved to %s", refs["refs/heads/main"])
			}
			if hasObject(t, cloneMirror(t, a, w.repo), own) {
				t.Error("an object pushed to the fork is in the parent")
			}
		})
	}
}

// The parent can be consolidated, collected and purged, and the fork still
// has every object; the fork can then be consolidated and collected, and the
// last blob goes only when nobody needs it. The other way round too.
func TestParentAndForkCanBeCollectedAndPurgedInAnyOrder(t *testing.T) {
	ctx := context.Background()
	for _, order := range []string{"parent-first", "fork-first"} {
		t.Run(order, func(t *testing.T) {
			w := newWorld(t, "fs", false)
			a := inproc(t, w.st)
			c := newClient(t)
			var ack []ackedPush
			for i := 0; i < 3; i++ {
				pushOK(t, c, a, w.repo, &ack, 4000)
			}
			w.newRepo(t, "frk1")
			if _, err := w.st.Fork(ctx, w.repo, "frk1"); err != nil {
				t.Fatal(err)
			}
			want := fingerprint(t, cloneMirror(t, a, "frk1"))
			pushOK(t, c, a, w.repo, &ack, 4000)
			pushOK(t, c, a, w.repo, &ack, 4000)
			// Consolidate and collect the parent with no grace at all.
			if ok, err := w.st.Maintain(ctx, w.repo, 2); err != nil || !ok {
				t.Fatalf("maintain the parent: %v %v", ok, err)
			}
			if _, err := w.st.Collect(ctx, w.repo, 0, 0); err != nil {
				t.Fatal(err)
			}
			if err := w.st.Wipe("frk1"); err != nil {
				t.Fatal(err)
			}
			if got := fingerprint(t, cloneMirror(t, a, "frk1")); got != want {
				t.Fatal("collecting the parent's retired packs changed the fork")
			}
			first, second := w.repo, "frk1"
			if order == "fork-first" {
				first, second = second, first
			}
			if _, err := w.st.Purge(ctx, first); err != nil {
				t.Fatal(err)
			}
			if _, err := w.st.Purge(ctx, first); err != nil { // twice is the same as once
				t.Fatal(err)
			}
			if err := w.st.Wipe(second); err != nil {
				t.Fatal(err)
			}
			dest := cloneMirror(t, a, second)
			if _, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dest, Args: []string{"fsck", "--full", "--strict", "--no-dangling"}, Timeout: time.Minute}); err != nil {
				t.Fatalf("after purging %s, %s is damaged: %v", first, second, err)
			}
			if second == "frk1" {
				if got := fingerprint(t, dest); got != want {
					t.Fatal("purging the parent changed the fork")
				}
			}
			// Now the second goes too: no blob of either is left behind.
			if _, err := w.st.Purge(ctx, second); err != nil {
				t.Fatal(err)
			}
			for _, repo := range []string{w.repo, "frk1"} {
				if _, err := w.st.Collect(ctx, repo, 0, 0); err != nil {
					t.Fatal(err)
				}
			}
			var left int
			if err := w.st.o.DB.QueryRow(ctx, `SELECT count(*) FROM repo_packs WHERE state <> 'orphan'`).Scan(&left); err != nil || left != 0 {
				t.Fatalf("%d packs are still not orphans: %v", left, err)
			}
			var keys []string
			rows, err := w.st.o.DB.Query(ctx, `SELECT blob_key FROM repo_packs`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var k string
				_ = rows.Scan(&k)
				keys = append(keys, k)
			}
			rows.Close()
			for _, k := range keys {
				if _, err := w.st.o.Blobs.Stat(ctx, k); !errors.Is(err, blobstore.ErrNotFound) {
					t.Errorf("blob %s is still stored after both repositories were purged: %v", k, err)
				}
			}
		})
	}
}

// Forking is all or nothing at every step: an injected failure at each point
// leaves the new repository empty and the parent untouched, and trying again
// makes the fork.
func TestForkFailsAtEveryStepAndTheRetryConverges(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	a := inproc(t, w.st)
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, a, w.repo, &ack, 4000)
	parent := fingerprint(t, cloneMirror(t, a, w.repo))
	for i, pt := range ForkPoints {
		id := "frk" + string(rune('a'+i))
		w.newRepo(t, id)
		_, err := w.st.Fork(WithFaults(ctx, &failAt{point: pt}), w.repo, id)
		if !errors.Is(err, ErrInjected) {
			t.Fatalf("%s: want the injected failure, got %v", pt, err)
		}
		if pt != PtForkAfter {
			if refs, _ := w.st.Refs(ctx, id); len(refs) != 0 {
				t.Errorf("%s: the failed fork has refs %v", pt, refs)
			}
			if g, _ := w.st.Ledger().Generation(ctx, id); g != 0 {
				t.Errorf("%s: the failed fork is at generation %d", pt, g)
			}
		}
		if pt == PtForkAfter {
			continue // the commit happened; the retry is refused as it already has content
		}
		if _, err := w.st.Fork(ctx, w.repo, id); err != nil {
			t.Fatalf("%s: the retry: %v", pt, err)
		}
		if got := fingerprint(t, cloneMirror(t, a, id)); got != parent {
			t.Errorf("%s: the fork made by the retry differs from the parent", pt)
		}
	}
	if got := fingerprint(t, cloneMirror(t, a, w.repo)); got != parent {
		t.Error("forking changed the parent")
	}
	// A fork is made once.
	if _, err := w.st.Fork(ctx, w.repo, "frka"); err == nil {
		t.Error("a repository that already has content was made a fork again")
	}
}

// Purge and collection fail at every step; running them again converges, the
// fork stays whole, and nothing is deleted that the fork needs.
func TestPurgeFailsAtEveryStepAndTheRetryConverges(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	a := inproc(t, w.st)
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, a, w.repo, &ack, 4000)
	pushOK(t, c, a, w.repo, &ack, 4000)
	w.newRepo(t, "frk1")
	if _, err := w.st.Fork(ctx, w.repo, "frk1"); err != nil {
		t.Fatal(err)
	}
	want := fingerprint(t, cloneMirror(t, a, "frk1"))
	pushOK(t, c, a, w.repo, &ack, 4000) // a pack only the parent has: purging it deletes a blob
	// The parent is purged, failing at each step in turn.
	for _, pt := range PurgePoints {
		if _, err := w.st.Purge(WithFaults(ctx, &failAt{point: pt}), w.repo); !errors.Is(err, ErrInjected) {
			t.Fatalf("%s: want the injected failure, got %v", pt, err)
		}
		if err := w.st.Wipe("frk1"); err != nil {
			t.Fatal(err)
		}
		if got := fingerprint(t, cloneMirror(t, a, "frk1")); got != want {
			t.Fatalf("%s: a failed purge of the parent changed the fork", pt)
		}
	}
	if _, err := w.st.Purge(ctx, w.repo); err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(t, cloneMirror(t, a, "frk1")); got != want {
		t.Fatal("the purge of the parent changed the fork")
	}
	for _, ok := range blobExists(t, w, "frk1") {
		if !ok {
			t.Error("a blob the fork needs was deleted")
		}
	}
}

// Forking while the parent is being pushed to gives a fork that is one
// generation of the parent, whole and sound.
func TestForkWhileTheParentIsPushedTo(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	a := inproc(t, w.st)
	c := newClient(t)
	var ack []ackedPush
	pushOK(t, c, a, w.repo, &ack, 3000)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 4; i++ {
			pushOK(t, c, a, w.repo, &ack, 3000)
		}
	}()
	for i := 0; i < 3; i++ {
		id := "frk" + string(rune('a'+i))
		w.newRepo(t, id)
		if _, err := w.st.Fork(ctx, w.repo, id); err != nil {
			t.Fatal(err)
		}
		dest := cloneMirror(t, a, id)
		if _, err := gitexec.Run(ctx, gitexec.Spec{GitDir: dest, Args: []string{"fsck", "--full", "--strict", "--no-dangling"}, Timeout: time.Minute}); err != nil {
			t.Fatalf("fork %s: %v", id, err)
		}
	}
	wg.Wait()
}
