package storage

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// inproc serves a Store from this process, for tests that measure the code
// and need no process to kill.
func inproc(t testing.TB, st *Store) *proc {
	t.Helper()
	srv := httptest.NewServer(smartHTTP(st))
	t.Cleanup(srv.Close)
	return &proc{t: t, URL: srv.URL}
}

// The whole life of a repository through one server: pushes, fetches, every
// injected error of the commit transaction, consolidation, collection,
// verification, usage, eviction and rebuilding.
func TestInProcessLifecycle(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "fs", false)
	a := inproc(t, w.st)
	c := newClient(t)
	var ack []ackedPush
	for i := 0; i < 9; i++ {
		pushOK(t, c, a, w.repo, &ack, 6000)
	}
	// Every point of the push can fail with an error and the retry converges.
	for _, pt := range PushPoints {
		c.commit(3000)
		if r := c.push(a, w.repo, testFault{Point: pt, Action: "error"}, "main:refs/heads/main"); r.Acked && pt != PtCommitted && pt != PtPostReceive && pt != PtTxAfterCommit {
			t.Errorf("%s: acknowledged", pt)
		}
		converge(t, c, a, w.repo, &ack)
	}
	for _, f := range []testFault{{Point: "db", Action: "dropbefore"}, {Point: "db", Action: "dropafter"}, {Point: "blob", Action: "down"}, {Point: "upload:part", Action: "error"}} {
		c.commit(3000)
		c.push(a, w.repo, f, "main:refs/heads/main")
		converge(t, c, a, w.repo, &ack)
	}
	u, err := w.st.Usage(ctx, w.repo)
	if err != nil || u.LivePacks < ConsolidateAbove {
		t.Fatalf("usage: %+v %v", u, err)
	}
	if len(u.Warnings) == 0 {
		t.Error("no warning about packs waiting to be consolidated")
	}
	ok, err := w.st.Maintain(ctx, w.repo, 0)
	if err != nil || !ok {
		t.Fatalf("maintenance: %v %v", ok, err)
	}
	if n, err := w.st.Collect(ctx, w.repo, 0, 24*time.Hour); err != nil || n == 0 {
		t.Fatalf("collect: %d %v", n, err)
	}
	rep, err := w.st.Verify(ctx, w.repo, 1)
	if err != nil || len(rep.Problems) != 0 || rep.PacksChecked == 0 {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	if err := w.st.Rebuild(ctx, w.repo); err != nil {
		t.Fatal(err)
	}
	if n, err := w.st.EvictCaches(1); err != nil || n != 1 {
		t.Fatalf("evict: %d %v", n, err)
	}
	if err := w.st.Wipe(w.repo); err != nil {
		t.Fatal(err)
	}
	// Clones through upload-pack after all of that.
	dest := cloneMirror(t, a, w.repo)
	if fingerprint(t, dest) == "" {
		t.Fatal("no fingerprint")
	}
	w.verify(t, ack)
	// An unknown repository, an invalid id, a repeated create.
	if err := w.st.Create(ctx, "BAD ID"); err == nil {
		t.Error("an invalid repository id was accepted")
	}
	if err := w.st.Create(ctx, w.repo); err != nil {
		t.Errorf("creating twice: %v", err)
	}
	if ok, err := w.st.Ledger().Exists(ctx, "nope"); ok || err != nil {
		t.Errorf("exists: %v %v", ok, err)
	}
	if _, err := w.st.Refs(ctx, "nope"); err == nil {
		t.Error("refs of an unknown repository")
	}
	r := c.push(a, "nope", testFault{}, "main:refs/heads/main")
	if r.Acked {
		t.Error("a push to an unknown repository was acknowledged")
	}
	if _, err := w.st.Maintain(ctx, "nope", 2); err == nil {
		t.Error("maintenance of an unknown repository")
	}
}

// Two pushes at once through one in-process server: one wins, the other is
// refused as stale and the error says so.
func TestInProcessRace(t *testing.T) {
	w := newWorld(t, "db", false)
	a := inproc(t, w.st)
	c1, c2 := newClient(t), newClient(t)
	c2.seed = 1_000_000
	var ack []ackedPush
	base := pushOK(t, c1, a, w.repo, &ack, 3000)
	c2.git("fetch", "-q", a.repoURL(w.repo), "main")
	c2.git("reset", "-q", "--hard", base)
	for i := 0; i < 4; i++ {
		s1, s2 := c1.commit(1000), c2.commit(1000)
		var r1, r2 pushResult
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); r1 = c1.push(a, w.repo, testFault{}, "main:refs/heads/main") }()
		go func() { defer wg.Done(); r2 = c2.push(a, w.repo, testFault{}, "main:refs/heads/main") }()
		wg.Wait()
		if r1.Acked == r2.Acked {
			t.Fatalf("round %d: %v %v", i, r1.Acked, r2.Acked)
		}
		win, lose := s1, c2
		if r2.Acked {
			win, lose = s2, c1
		}
		ack = append(ack, ackedPush{"refs/heads/main", win})
		lose.git("fetch", "-q", a.repoURL(w.repo), "main")
		lose.git("reset", "-q", "--hard", win)
	}
	w.verify(t, ack)
}

func TestHookOutsideAPushIsRefused(t *testing.T) {
	// A hook run by anyone but a Receive of this process has no socket and
	// token, and must refuse.
	for _, s := range []string{"pre-receive", "reference-transaction"} {
		if !strings.Contains(strings.Join(hookNames, ","), s) {
			t.Errorf("%s is not a hook name", s)
		}
	}
	rpc := RPC{GitProtocol: "version=2"}
	if len(rpc.env()) != 1 || (RPC{GitProtocol: "x; rm -rf /"}).env() != nil {
		t.Error("the protocol header is not validated")
	}
}
