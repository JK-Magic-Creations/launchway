package storagespike

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func newSystem(t testing.TB, kind string, pg bool) System {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	addr := filepath.Join(root, "ledger.db")
	if pg {
		addr = testdb.New(t)
	}
	switch kind {
	case "A":
		s, err := NewVolumeSystem(ctx, root, addr, "rep1", "")
		if err != nil {
			t.Fatal(err)
		}
		return s
	case "B-fs", "B-db":
		s, err := NewLedgerSystem(ctx, root, addr, kind[2:], "rep1")
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	t.Fatalf("unknown system %s", kind)
	return nil
}

func TestSmoke(t *testing.T) {
	for _, kind := range []string{"A", "B-fs", "B-db"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			sys := newSystem(t, kind, false)
			s1, _ := sys.NewServer("s1")
			s2, _ := sys.NewServer("s2")
			c, err := NewClient(ctx, filepath.Join(t.TempDir(), "w"))
			if err != nil {
				t.Fatal(err)
			}
			sha, err := c.Commit(ctx, "a.bin", 4096, 1)
			if err != nil {
				t.Fatal(err)
			}
			out, err := s1.Push(ctx, c.Dir, Fault{}, "main:refs/heads/main")
			if err != nil || !out.Acked {
				t.Fatalf("push: %+v %v", out, err)
			}
			auth, err := sys.Authoritative(ctx)
			if err != nil || auth["refs/heads/main"] != sha {
				t.Fatalf("authoritative %v %v", auth, err)
			}
			dest := filepath.Join(t.TempDir(), "clone")
			if err := s2.Clone(ctx, dest); err != nil {
				t.Fatal(err)
			}
			if err := Fsck(ctx, dest); err != nil {
				t.Fatal(err)
			}
			refs, _ := RefsOf(ctx, dest)
			if refs["refs/heads/main"] != sha {
				t.Fatalf("clone from s2: %v", refs)
			}
		})
	}
}
