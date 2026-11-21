package storagespike

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// faultPool is every injection a candidate can be given: a kill at each step
// of the push, the database dropping its connection, the blob store failing.
func faultPool(kind string) []Fault {
	var pool []Fault
	if kind == "A" {
		for _, pt := range []string{PtPreReceive, PtPrepared, PtCommitted, PtPostReceive} {
			pool = append(pool, Fault{Point: pt, Action: "kill"})
		}
		pool = append(pool, Fault{Point: "ledger", Action: "error"}, Fault{Point: PtPrepared, Action: "error"}, Fault{Point: PtPreReceive, Action: "error"})
		return pool
	}
	for _, pt := range PushPoints {
		pool = append(pool, Fault{Point: pt, Action: "kill"}, Fault{Point: pt, Action: "error"})
	}
	pool = append(pool,
		Fault{Point: "db", Action: "dropbefore"}, Fault{Point: "db", Action: "dropafter"},
		Fault{Point: "blob", Action: "down"}, Fault{Point: "upload:part", Action: "error", Arg: "1"},
		Fault{Point: "upload:data", Action: "truncate"}, Fault{Point: "upload:data", Action: "flip"},
		Fault{Point: "blob", Action: "slow", Arg: "20ms"})
	return pool
}

// The campaign: many rounds of pushes, alternating between two servers, each
// with a random failure injected into the real git processes of that push;
// after a failed push the client retries, on either server; now and then a
// server's local state is wiped or the repository is repacked. At the end,
// and along the way, every property is checked from a cold server. The number
// of rounds is FORGE_SPIKE_ROUNDS (default 100; the spike report ran 1,150).
func TestInjectionCampaign(t *testing.T) {
	rounds := envInt("FORGE_SPIKE_ROUNDS", 100)
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
			if !testdb.Postgres() || os.Getenv("FORGE_SPIKE_CAMPAIGN_PG") == "" {
				continue
			}
		}
		for _, kind := range kinds {
			if pg && kind == "A" {
				continue
			}
			t.Run(name+"/"+kind, func(t *testing.T) {
				ctx := context.Background()
				e := newEnv(t, kind, pg)
				if ls, ok := e.sys.(*LedgerSystem); ok {
					ls.VerifyUpload = true // the recommended setting
				}
				s := make([]Server, 2)
				for i := range s {
					s[i], _ = e.sys.NewServer(fmt.Sprintf("s%d", i+1))
				}
				rng := rand.New(rand.NewSource(int64(envInt("FORGE_SPIKE_SEED", 7))))
				pool := faultPool(kind)
				type tally struct{ n, acked, committedUnacked, retries int }
				stats := map[string]*tally{}
				injected := 0
				for i := 0; i < rounds; i++ {
					var f Fault
					if rng.Intn(10) > 0 { // one round in ten has no fault
						f = pool[rng.Intn(len(pool))]
						injected++
					}
					srv := s[i%2]
					size := 1000 + rng.Intn(150000)
					if rng.Intn(20) == 0 {
						size = 3 << 20
					}
					out, sha := e.commitAndPush(t, srv, f, size)
					tl := stats[f.String()]
					if tl == nil {
						tl = &tally{}
						stats[f.String()] = tl
					}
					tl.n++
					if out.Acked {
						tl.acked++
					} else {
						if auth, _ := e.sys.Authoritative(ctx); auth["refs/heads/main"] == sha {
							tl.committedUnacked++ // done, but the client was not told
						}
						// The retry goes to the other server half the time.
						other := s[(i+rng.Intn(2))%2]
						e.converge(t, other)
						tl.retries++
					}
					switch {
					case i%40 == 39:
						verifyProperties(t, e.sys, e.ack)
					case i%17 == 16 && kind != "A":
						s[rng.Intn(2)].WipeLocal()
					case i%23 == 22:
						if err := e.sys.Maintain(ctx); err != nil {
							t.Fatalf("maintenance: %v", err)
						}
					}
				}
				verifyProperties(t, e.sys, e.ack)
				keys := make([]string, 0, len(stats))
				for k := range stats {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				t.Logf("%s on %s: %d rounds, %d with a failure injected, %d pushes acknowledged, every property held", kind, name, rounds, injected, len(e.ack))
				for _, k := range keys {
					tl := stats[k]
					if k == "" {
						k = "(no fault)"
					}
					t.Logf("  %-36s rounds %-3d acked %-3d done-but-unacked %-3d retried %d", k, tl.n, tl.acked, tl.committedUnacked, tl.retries)
				}
				_ = filepath.Join
			})
		}
	}
}

// The store marker: a server whose blob store is not the one the database was
// set up with refuses to host, rather than splitting the repositories.
func TestStoreMarkerRefusesAForeignBlobStore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "B-fs", false)
	s1, _ := e.sys.NewServer("s1")
	e.commitAndPush(t, s1, Fault{}, 4096)
	ls := e.sys.(*LedgerSystem)
	// A second server is configured with another, empty blob directory.
	cfg := ls.config("rogue", filepath.Join(ls.Root, "servers", "rogue", "repo.git"))
	cfg.BlobDir = filepath.Join(t.TempDir(), "other-blobs")
	r, err := OpenReplica(cfg, Fault{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := r.Sync(ctx); err == nil {
		t.Fatal("a server with a foreign blob store was allowed to host")
	} else {
		t.Logf("refused: %v", err)
	}
}
