package storagespike

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// kinds are the systems every property is run against.
var kinds = []string{"A", "B-fs", "B-db"}

type env struct {
	sys    System
	c      *Client
	ack    []acked
	seed   int64
	system string
}

func newEnv(t *testing.T, kind string, pg bool) *env {
	t.Helper()
	ctx := context.Background()
	c, err := NewClient(ctx, filepath.Join(t.TempDir(), "client"))
	if err != nil {
		t.Fatal(err)
	}
	return &env{sys: newSystem(t, kind, pg), c: c, system: kind}
}

// commitAndPush adds a commit and pushes it to the server, recording an
// acknowledged push.
func (e *env) commitAndPush(t testing.TB, srv Server, f Fault, size int) (PushOutcome, string) {
	t.Helper()
	ctx := context.Background()
	e.seed++
	sha, err := e.c.Commit(ctx, fmt.Sprintf("f%d.bin", e.seed), size, e.seed)
	if err != nil {
		t.Fatal(err)
	}
	return e.push(t, srv, f), sha
}

func (e *env) push(t testing.TB, srv Server, f Fault) PushOutcome {
	t.Helper()
	ctx := context.Background()
	head, _ := e.c.Head(ctx)
	out, err := srv.Push(ctx, e.c.Dir, f, "main:refs/heads/main")
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if out.Acked {
		e.ack = append(e.ack, acked{"refs/heads/main", head})
	}
	return out
}

// converge pushes again, with no fault, until the server acknowledges: after
// any failure a client's retry must succeed.
func (e *env) converge(t testing.TB, srv Server) {
	t.Helper()
	for i := 0; i < 3; i++ {
		if out := e.push(t, srv, Fault{}); out.Acked {
			return
		} else if i == 2 {
			t.Fatalf("a retry after the failure was still refused: %s", out.Output)
		}
	}
}

// Kill -9 at every step of a push, on every candidate: the client's retry
// succeeds, and nothing acknowledged is lost and no ref points at missing
// objects, seen from a server that holds nothing.
func TestKillAtEveryStepOfAPush(t *testing.T) {
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t, kind, false)
			srv, _ := e.sys.NewServer("s1")
			if out, _ := e.commitAndPush(t, srv, Fault{}, 8192); !out.Acked {
				t.Fatalf("the first push: %s", out.Output)
			}
			for _, pt := range PushPoints {
				out, _ := e.commitAndPush(t, srv, Fault{Point: pt, Action: "kill"}, 8192)
				reached := !out.Acked
				e.converge(t, srv)
				t.Logf("%-28s kill: push acked=%v (fault reached=%v), retry converged", pt, out.Acked, reached)
				verifyProperties(t, e.sys, e.ack)
			}
		})
	}
}

// The database drops the connection in the middle of the commit: before it
// (nothing committed) and after it (committed, the answer lost). Both
// candidates must keep every acknowledged push; the second case is the
// ambiguous one: the client is told "failed" though it happened.
func TestDatabaseConnectionDropDuringCommit(t *testing.T) {
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t, kind, false)
			srv, _ := e.sys.NewServer("s1")
			e.commitAndPush(t, srv, Fault{}, 4096)
			for _, f := range []Fault{{Point: "db", Action: "dropbefore"}, {Point: "db", Action: "dropafter"}, {Point: "ledger", Action: "error"}} {
				out, _ := e.commitAndPush(t, srv, f, 4096)
				auth, _ := e.sys.Authoritative(context.Background())
				head, _ := e.c.Head(context.Background())
				t.Logf("%-18s push acked=%-5v refs of record at the new commit=%v", f.String(), out.Acked, auth["refs/heads/main"] == head)
				e.converge(t, srv)
				verifyProperties(t, e.sys, e.ack)
			}
		})
	}
}

// Two servers push the same branch at once, from the same base: exactly one
// is acknowledged, the other is refused as stale, and the refs of record hold
// the winner. On PostgreSQL this repeats 100 times; elsewhere fewer.
func TestConcurrentPushesToOneRefSerialize(t *testing.T) {
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
			if !testdb.Postgres() {
				continue
			}
		}
		for _, kind := range kinds {
			t.Run(name+"/"+kind, func(t *testing.T) {
				rounds := envInt("FORGE_SPIKE_RACE_ROUNDS", 12)
				if pg {
					rounds = envInt("FORGE_SPIKE_RACE_ROUNDS", 100)
				}
				ctx := context.Background()
				e := newEnv(t, kind, pg)
				s1, _ := e.sys.NewServer("s1")
				s2, _ := e.sys.NewServer("s2")
				c2, err := NewClient(ctx, filepath.Join(t.TempDir(), "client2"))
				if err != nil {
					t.Fatal(err)
				}
				e.commitAndPush(t, s1, Fault{}, 4096)
				var wins1, wins2, stale int
				for i := 0; i < rounds; i++ {
					// Both clients start from the commit of record.
					base, _ := e.c.Head(ctx)
					if err := c2.Fetch(ctx, e.c.Dir, "main"); err != nil {
						t.Fatal(err)
					}
					if err := c2.Reset(ctx, base); err != nil {
						t.Fatal(err)
					}
					sha1, err := e.c.Commit(ctx, fmt.Sprintf("a%d.bin", i), 2048, int64(i)*2+1)
					if err != nil {
						t.Fatal(err)
					}
					sha2, err := c2.Commit(ctx, fmt.Sprintf("b%d.bin", i), 2048, int64(i)*2+2)
					if err != nil {
						t.Fatal(err)
					}
					var wg sync.WaitGroup
					var o1, o2 PushOutcome
					wg.Add(2)
					go func() { defer wg.Done(); o1, _ = s1.Push(ctx, e.c.Dir, Fault{}, "main:refs/heads/main") }()
					go func() { defer wg.Done(); o2, _ = s2.Push(ctx, c2.Dir, Fault{}, "main:refs/heads/main") }()
					wg.Wait()
					if o1.Acked == o2.Acked {
						t.Fatalf("round %d: both acked=%v: exactly one push must win\nfirst: %s\nsecond: %s", i, o1.Acked, o1.Output, o2.Output)
					}
					loser := o1
					if o1.Acked {
						loser = o2
					}
					if !loser.Stale {
						t.Fatalf("round %d: the loser was not refused as stale: %s", i, loser.Output)
					}
					stale++
					auth, _ := e.sys.Authoritative(ctx)
					want := sha2
					if o1.Acked {
						want, wins1 = sha1, wins1+1
					} else {
						wins2++
					}
					if auth["refs/heads/main"] != want {
						t.Fatalf("round %d: the refs of record hold %s, the winner pushed %s", i, short(auth["refs/heads/main"]), short(want))
					}
					e.ack = append(e.ack, acked{"refs/heads/main", want})
					// The client that lost takes the winner's commit and the other continues from it.
					if o1.Acked {
						if err := c2.Fetch(ctx, e.c.Dir, "main"); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := e.c.Fetch(ctx, c2.Dir, "main"); err != nil {
							t.Fatal(err)
						}
						if err := e.c.Reset(ctx, want); err != nil {
							t.Fatal(err)
						}
					}
					if o1.Acked {
						if err := c2.Reset(ctx, want); err != nil {
							t.Fatal(err)
						}
					}
				}
				t.Logf("%d rounds: server 1 won %d, server 2 won %d, %d refused as stale", rounds, wins1, wins2, stale)
				verifyProperties(t, e.sys, e.ack)
			})
		}
	}
}

// Pushes to different branches of one repository from two servers both land.
func TestConcurrentPushesToDifferentRefsBothLand(t *testing.T) {
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t, kind, false)
			s1, _ := e.sys.NewServer("s1")
			s2, _ := e.sys.NewServer("s2")
			c2, _ := NewClient(ctx, filepath.Join(t.TempDir(), "client2"))
			e.commitAndPush(t, s1, Fault{}, 4096)
			for i := 0; i < 6; i++ {
				a, _ := e.c.Commit(ctx, fmt.Sprintf("x%d", i), 1024, int64(100+i))
				b, _ := c2.Commit(ctx, fmt.Sprintf("y%d", i), 1024, int64(200+i))
				var wg sync.WaitGroup
				var o1, o2 PushOutcome
				wg.Add(2)
				go func() { defer wg.Done(); o1, _ = s1.Push(ctx, e.c.Dir, Fault{}, "main:refs/heads/main") }()
				go func() {
					defer wg.Done()
					o2, _ = s2.Push(ctx, c2.Dir, Fault{}, fmt.Sprintf("main:refs/heads/other%d", i))
				}()
				wg.Wait()
				if !o1.Acked || !o2.Acked {
					t.Fatalf("round %d: %v %v\n%s\n%s", i, o1.Acked, o2.Acked, o1.Output, o2.Output)
				}
				e.ack = append(e.ack, acked{"refs/heads/main", a}, acked{fmt.Sprintf("refs/heads/other%d", i), b})
			}
			verifyProperties(t, e.sys, e.ack)
		})
	}
}

// A fetch right after a push on another server sees it (read-your-writes).
func TestReadYourWritesAcrossServers(t *testing.T) {
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			e := newEnv(t, kind, false)
			s1, _ := e.sys.NewServer("s1")
			s2, _ := e.sys.NewServer("s2")
			for i := 0; i < 8; i++ {
				w, r := s1, s2
				if i%2 == 1 {
					w, r = s2, s1
				}
				out, sha := e.commitAndPush(t, w, Fault{}, 2048)
				if !out.Acked {
					t.Fatalf("round %d: %s", i, out.Output)
				}
				dest := filepath.Join(t.TempDir(), "r.git")
				if err := r.Clone(ctx, dest); err != nil {
					t.Fatal(err)
				}
				refs, _ := RefsOf(ctx, dest)
				if refs["refs/heads/main"] != sha {
					t.Fatalf("round %d: a fetch on the other server right after the push saw %s, not %s", i, short(refs["refs/heads/main"]), short(sha))
				}
			}
			verifyProperties(t, e.sys, e.ack)
		})
	}
}

// Candidate B only: the blob store fails, is slow, or lies.
func TestBlobStoreFaults(t *testing.T) {
	ctx := context.Background()
	type tc struct {
		f       Fault
		verify  bool
		wantAck bool
		note    string
	}
	for _, c := range []tc{
		{Fault{Point: "blob", Action: "down"}, false, false, "the blob store is unavailable: the push fails before the commit"},
		{Fault{Point: "upload:part", Action: "error", Arg: "1"}, false, false, "an upload part fails: the push fails before the commit"},
		{Fault{Point: "upload:data", Action: "truncate"}, false, false, "a pack is stored cut short, success reported: caught by the size check"},
		{Fault{Point: "blob", Action: "slow", Arg: "150ms"}, false, true, "a slow store: slower, not wrong"},
		{Fault{Point: "upload:data", Action: "flip"}, true, false, "one byte of the stored pack is wrong, same size: caught only by reading it back"},
	} {
		t.Run(strings.ReplaceAll(c.f.String(), "=", "_"), func(t *testing.T) {
			e := newEnv(t, "B-fs", false)
			e.sys.(*LedgerSystem).VerifyUpload = c.verify
			srv, _ := e.sys.NewServer("s1")
			e.commitAndPush(t, srv, Fault{}, 4096)
			out, _ := e.commitAndPush(t, srv, c.f, 20000)
			if out.Acked != c.wantAck {
				t.Fatalf("acked=%v, want %v: %s", out.Acked, c.wantAck, out.Output)
			}
			t.Logf("%s: acked=%v", c.note, out.Acked)
			auth, _ := e.sys.Authoritative(ctx)
			head, _ := e.c.Head(ctx)
			if !out.Acked && auth["refs/heads/main"] == head {
				t.Errorf("the push failed but the refs of record moved")
			}
			e.converge(t, srv)
			verifyProperties(t, e.sys, e.ack)
		})
	}
}

// The silent case without read-back verification: the store keeps a wrong
// pack of the right size and says yes. The push is acknowledged, and a cold
// server cannot be built afterwards. This is the failure the design must not
// have, shown on purpose, so the recommendation can name its cure
// (VerifyUpload, or a store that checksums on write).
func TestSilentCorruptionIsCaughtOnlyByReadBack(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "B-fs", false)
	srv, _ := e.sys.NewServer("s1")
	out, _ := e.commitAndPush(t, srv, Fault{Point: "upload:data", Action: "flip"}, 20000)
	if !out.Acked {
		t.Skipf("the corruption was caught at push time: %s", out.Output)
	}
	cold, _ := e.sys.NewServer("cold")
	err := cold.Clone(ctx, filepath.Join(t.TempDir(), "x.git"))
	if err == nil {
		t.Fatal("expected the cold server to refuse a corrupt pack")
	}
	t.Logf("WITHOUT read-back verification: push acknowledged, cold server fails: %v", err)
	if !strings.Contains(err.Error(), "does not match its recorded SHA-256") {
		t.Errorf("unexpected failure: %v", err)
	}
}

// The blob store is down: a server with a current cache keeps serving reads,
// a cold one cannot be built (it says so; it does not serve wrong data).
func TestBlobStoreDownReadsFromCache(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "B-fs", false)
	warm, _ := e.sys.NewServer("warm")
	e.commitAndPush(t, warm, Fault{}, 4096)
	if err := warm.Clone(ctx, filepath.Join(t.TempDir(), "w.git")); err != nil {
		t.Fatal(err)
	}
	ls := e.sys.(*LedgerSystem)
	if err := os.Rename(filepath.Join(ls.Root, "blobs"), filepath.Join(ls.Root, "blobs.away")); err != nil {
		t.Fatal(err)
	}
	if err := warm.Clone(ctx, filepath.Join(t.TempDir(), "w2.git")); err != nil {
		t.Errorf("a warm server must keep serving while the blob store is away: %v", err)
	}
	cold, _ := e.sys.NewServer("cold")
	if err := cold.Clone(ctx, filepath.Join(t.TempDir(), "c.git")); err == nil {
		t.Error("a cold server served from a blob store that is away")
	}
}
