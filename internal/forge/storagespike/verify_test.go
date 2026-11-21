package storagespike

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

// acked is a push the server said yes to: the ref and the commit it was set to.
type acked struct{ ref, sha string }

var coldN atomic.Int64

// envInt reads an integer setting, with a default.
func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// verifyProperties checks, from a server that holds nothing (a cold one),
// what the design must guarantee after any sequence of pushes and failures:
//
//	no acknowledged push is lost: its ref exists and holds the commit pushed
//	  or a descendant of it, and the commit's whole history is fetchable;
//	no ref points at missing objects: a full fsck of what a fresh clone got;
//	the refs a fresh server serves are exactly the refs of record.
func verifyProperties(t testing.TB, sys System, ack []acked) {
	t.Helper()
	ctx := context.Background()
	auth, err := sys.Authoritative(ctx)
	if err != nil {
		t.Fatalf("reading the refs of record: %v", err)
	}
	cold, err := sys.NewServer(fmt.Sprintf("cold-%d", coldN.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "verify.git")
	if err := cold.Clone(ctx, dest); err != nil {
		t.Fatalf("a cold server cannot serve the repository: %v", err)
	}
	if err := Fsck(ctx, dest); err != nil {
		t.Fatalf("NO REF MAY POINT AT MISSING OBJECTS: fsck of a fresh clone: %v", err)
	}
	got, err := RefsOf(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	for n, sha := range auth {
		if got[n] != sha {
			t.Errorf("a cold server serves %s at %s, the refs of record say %s", n, short(got[n]), short(sha))
		}
	}
	for n := range got {
		if _, ok := auth[n]; !ok {
			t.Errorf("a cold server serves %s, which the refs of record do not have", n)
		}
	}
	for _, a := range ack {
		cur, ok := auth[a.ref]
		if !ok {
			t.Errorf("ACKNOWLEDGED PUSH LOST: %s (pushed %s) does not exist", a.ref, short(a.sha))
			continue
		}
		if cur != a.sha && !IsAncestor(ctx, dest, a.sha, cur) {
			t.Errorf("ACKNOWLEDGED PUSH LOST: %s is at %s, which does not contain the acknowledged %s", a.ref, short(cur), short(a.sha))
		}
	}
}
