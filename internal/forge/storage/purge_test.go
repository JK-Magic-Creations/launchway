package storage

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// stopAt fails the removal at its nth visit to a point, once: the same as the
// process ending there, because everything a removal changes is in the database
// and the blob store.
type stopAt struct {
	point string
	nth   int
	seen  int
	fired bool
}

func (s *stopAt) Hit(p string) error {
	if p != s.point || s.fired {
		return nil
	}
	s.seen++
	if s.seen == s.nth {
		s.fired = true
		return injected(p)
	}
	return nil
}
func (s *stopAt) Drop(string) error { return nil }

func countRows(t testing.TB, w *world, table, repo string) int {
	t.Helper()
	var n int
	if err := w.eng.Database().QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE repo_id = ?`, repo).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// blobsOf returns the blob keys of a repository's packs.
func blobsOf(t testing.TB, w *world, repo string) []string {
	t.Helper()
	rows, err := w.eng.Database().Query(context.Background(), `SELECT blob_key FROM repo_packs WHERE repo_id = ?`, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

func blobExists(t testing.TB, w *world, key string) bool {
	t.Helper()
	_, err := w.st.o.Blobs.Stat(context.Background(), key)
	if err == nil {
		return true
	}
	if errors.Is(err, blobstore.ErrNotFound) {
		return false
	}
	t.Fatal(err)
	return false
}

// pushed makes a repository with three packs (three pushes, no maintenance).
func pushed(t testing.TB, w *world, repo string) []string {
	t.Helper()
	if err := w.st.Create(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	a := inproc(t, w.st)
	c := newClient(t)
	var ack []ackedPush
	for i := 0; i < 3; i++ {
		pushOK(t, c, a, repo, &ack, 4000)
	}
	keys := blobsOf(t, w, repo)
	if len(keys) < 3 {
		t.Fatalf("%d packs, want at least 3", len(keys))
	}
	return keys
}

// Permanent removal stopped at every point, at each pack, and run again: it
// always finishes, the repository's refs, pack rows, ledger rows and blobs are
// gone, and a repository next to it is never touched (its refs, its packs, its
// blobs, and a clone from a server that holds nothing).
func TestPurgeStoppedAtEveryStepFinishesWhenRunAgain(t *testing.T) {
	for _, kind := range blobKinds {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t, kind, false)
			neighbour := pushed(t, w, "neighbour")
			for _, pt := range PurgePoints {
				for nth := 1; nth <= 2; nth++ {
					repo := fmt.Sprintf("gone-%d-%d", len(pt), nth) + fmt.Sprint(pt[len(pt)-3:])
					keys := pushed(t, w, repo)
					f := &stopAt{point: pt, nth: nth}
					_, err := w.st.Purge(WithFaults(ctx, f), repo)
					if f.fired && !errors.Is(err, ErrInjected) {
						t.Fatalf("%s #%d: stopped, but the removal said %v", pt, nth, err)
					}
					if !f.fired && err != nil {
						t.Fatalf("%s #%d: %v", pt, nth, err)
					}
					if f.fired {
						// A half-removed repository is still known to the ledger, so it can be found and finished.
						if known, _ := w.st.Ledger().Exists(ctx, repo); !known && countRows(t, w, "repo_packs", repo) > 0 {
							t.Fatalf("%s #%d: pack rows left behind a repository the ledger forgot", pt, nth)
						}
					}
					// A new process: a new store on the same database and blobs.
					st2, eng2, err := openStore(ctx, serverEnv{DB: w.db, Blobs: w.blobs, Cache: w.root + "/again", Name: "inproc"})
					if err != nil {
						t.Fatal(err)
					}
					res, err := st2.Purge(ctx, repo)
					if err != nil {
						t.Fatalf("%s #%d: running it again: %v", pt, nth, err)
					}
					_ = res
					for _, table := range []string{"repo_refs", "repo_packs", "repo_generation", "repo_maintenance"} {
						if n := countRows(t, w, table, repo); n != 0 {
							t.Errorf("%s #%d: %d rows left in %s", pt, nth, n, table)
						}
					}
					for _, k := range keys {
						if blobExists(t, w, k) {
							t.Errorf("%s #%d: the blob %s is still there", pt, nth, k)
						}
					}
					// Running it a third time is nothing to do.
					if res, err := st2.Purge(ctx, repo); err != nil || res.Packs != 0 {
						t.Errorf("a repository already removed: %+v %v", res, err)
					}
					eng2.Close()
					for _, k := range neighbour {
						if !blobExists(t, w, k) {
							t.Fatalf("%s #%d: a blob of the neighbouring repository was deleted", pt, nth)
						}
					}
				}
			}
			if n := countRows(t, w, "repo_packs", "neighbour"); n != len(neighbour) {
				t.Errorf("the neighbour has %d pack rows, want %d", n, len(neighbour))
			}
			cold := w.server("cold")
			dest := cloneMirror(t, cold, "neighbour")
			if fingerprint(t, dest) == "" {
				t.Error("the neighbour cannot be cloned")
			}
			cold.Kill()
		})
	}
}

// A blob that a pack of another repository still names is never deleted.
func TestPurgeKeepsABlobAnotherRepositoryNames(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	keys := pushed(t, w, "doomed")
	if err := w.st.Create(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	// Another repository's manifest names the first blob of the doomed one.
	if _, err := w.eng.Database().Exec(ctx, `INSERT INTO repo_packs(repo_id, pack_id, blob_key, bytes, objects, state, created_at) VALUES('other','shared',?,1,1,'live',1)`, keys[0]); err != nil {
		t.Fatal(err)
	}
	res, err := w.st.Purge(ctx, "doomed")
	if err != nil {
		t.Fatal(err)
	}
	if res.BlobsKept != 1 || res.BlobsDeleted != len(keys)-1 {
		t.Errorf("%+v, want one blob kept and %d deleted", res, len(keys)-1)
	}
	if !blobExists(t, w, keys[0]) {
		t.Error("a blob another repository names was deleted")
	}
	for _, k := range keys[1:] {
		if blobExists(t, w, k) {
			t.Errorf("%s was kept", k)
		}
	}
}

// Another server holding the lease blocks the removal until it ends.
func TestPurgeRefusesWhileAnotherServerHoldsTheLease(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	keys := pushed(t, w, "leased")
	if ok, err := w.st.Ledger().Lease(ctx, "leased", "another-server", 10*60*1e9); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, err := w.st.Purge(ctx, "leased"); !errors.Is(err, ErrPurgeBusy) {
		t.Fatalf("%v, want ErrPurgeBusy", err)
	}
	if !blobExists(t, w, keys[0]) {
		t.Error("a blob went although the removal was refused")
	}
}

// A parent and its fork share pack blobs by reference. The permanent removal of either, stopped at every step and
// run again, never deletes a blob the other still names (the other stays whole: its rows, its blobs and a clone
// that passes fsck); and when the second is removed too, no blob of either is left. Both orders.
func TestPurgeOfAForkedPairStoppedAtEveryStepKeepsWhatTheOtherNames(t *testing.T) {
	for _, order := range []string{"parent-first", "fork-first"} {
		t.Run(order, func(t *testing.T) {
			ctx := context.Background()
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
			pushOK(t, c, a, w.repo, &ack, 4000) // a pack only the parent has
			parentKeys, forkKeys := blobsOf(t, w, w.repo), blobsOf(t, w, "frk1")
			first, second := w.repo, "frk1"
			firstKeys, secondKeys := parentKeys, forkKeys
			if order == "fork-first" {
				first, second, firstKeys, secondKeys = second, first, secondKeys, firstKeys
			}
			wantSecond := fingerprint(t, cloneMirror(t, a, second))
			// Stop the first one's removal at every point of its steps (the first and the second time the point
			// is reached), then finish it in a new process. A fresh pair is made for every try.
			for pi, pt := range PurgePoints {
				for nth := 1; nth <= 2; nth++ {
					p, f := fmt.Sprintf("p%d-%d", pi, nth), fmt.Sprintf("f%d-%d", pi, nth)
					w.newRepo(t, p)
					for i := 0; i < 3; i++ {
						pushOK(t, c, a, p, &ack, 4000)
					}
					w.newRepo(t, f)
					if _, err := w.st.Fork(ctx, p, f); err != nil {
						t.Fatal(err)
					}
					pushOK(t, c, a, p, &ack, 4000)
					victim, keeper := p, f
					if order == "fork-first" {
						victim, keeper = f, p
					}
					keeperKeys := blobsOf(t, w, keeper)
					want := fingerprint(t, cloneMirror(t, a, keeper))
					fault := &stopAt{point: pt, nth: nth}
					if _, err := w.st.Purge(WithFaults(ctx, fault), victim); fault.fired && !errors.Is(err, ErrInjected) {
						t.Fatalf("%s %s #%d: stopped, but the removal said %v", order, pt, nth, err)
					}
					st2, eng2, err := openStore(ctx, serverEnv{DB: w.db, Blobs: w.blobs, Cache: w.root + "/again-" + p, Name: "inproc"})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := st2.Purge(ctx, victim); err != nil {
						t.Fatalf("%s %s #%d: running it again: %v", order, pt, nth, err)
					}
					eng2.Close()
					for _, k := range keeperKeys {
						if !blobExists(t, w, k) {
							t.Fatalf("%s %s #%d: a blob the other repository names was deleted", order, pt, nth)
						}
					}
					if got := fingerprint(t, cloneMirror(t, a, keeper)); got != want {
						t.Fatalf("%s %s #%d: the removal changed the repository next to it", order, pt, nth)
					}
					// Removing the other one too leaves nothing of either.
					if _, err := w.st.Purge(ctx, keeper); err != nil {
						t.Fatal(err)
					}
					if n := countRows(t, w, "repo_packs", p) + countRows(t, w, "repo_packs", f); n != 0 {
						t.Fatalf("%s %s #%d: %d pack rows left", order, pt, nth, n)
					}
					for _, k := range keeperKeys {
						if blobExists(t, w, k) {
							t.Errorf("%s %s #%d: the blob %s is left behind", order, pt, nth, k)
						}
					}
				}
			}
			// The pair this test started with is untouched by all of that.
			for _, k := range append(append([]string{}, firstKeys...), secondKeys...) {
				if !blobExists(t, w, k) {
					t.Fatalf("a blob of the first pair was deleted by the removal of another pair")
				}
			}
			if got := fingerprint(t, cloneMirror(t, a, second)); got != wantSecond {
				t.Fatal("the pair's repository changed")
			}
		})
	}
}
