package graph

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// synthetic loads a layered graph straight into the tables: repositories
// holding components that publish packages and depend on packages of lower
// layers, images built from components, and deployments of the images. It
// returns the numbers of nodes and edges.
func synthetic(t testing.TB, s *Store, repos, layers int) (nodes, edges int) {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	tx, err := s.DB.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var base int64
	_ = tx.QueryRow(bg, `SELECT coalesce(max(nid), 0) FROM graph_nodes`).Scan(&base)
	next := base
	addNode := func(kind, key, subject string) int64 {
		next++
		if _, err := tx.Exec(bg, `INSERT INTO graph_nodes(nid, org, kind, key, name, subject, origin, scope, first_seen, last_seen) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			next, "", kind, key, key, subject, "synthetic", "all", 1, 1); err != nil {
			t.Fatal(err)
		}
		nodes++
		return next
	}
	addEdge := func(kind string, src, dst int64, rev int, subject string, q string) {
		if _, err := tx.Exec(bg, `INSERT INTO graph_edges(org, kind, src, dst, rev, qualifier, constraint_text, resolved, subject, origin, scope, valid_from) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			"", kind, src, dst, rev, q, "^1", "1.0.0", subject, "synthetic", "all", 1); err != nil {
			t.Fatal(err)
		}
		edges++
	}
	perLayer := make([][]int64, layers)
	for r := 0; r < repos; r++ {
		sub := fmt.Sprintf("repo:org/r%d", r)
		repo := addNode("repository", fmt.Sprintf("org/r%d", r), sub)
		layer := r * layers / repos
		for c := 0; c < 4; c++ {
			comp := addNode("component", fmt.Sprintf("org/r%d:c%d", r, c), sub)
			addEdge("contains", repo, comp, 0, sub, "")
			if c < 2 {
				p := addNode("package", fmt.Sprintf("go:example.test/r%d/c%d", r, c), "")
				addEdge("contains", comp, p, 0, sub, "go.mod")
				perLayer[layer] = append(perLayer[layer], p)
			}
			if layer > 0 {
				for d := 0; d < 9; d++ {
					l := rng.Intn(layer)
					if len(perLayer[l]) == 0 {
						continue
					}
					p := perLayer[l][rng.Intn(len(perLayer[l]))]
					addEdge("depends-on", p, comp, 1, sub, fmt.Sprintf("m%d", d))
				}
			}
			if c == 3 {
				img := addNode("image", fmt.Sprintf("reg.test/org/r%d", r), "")
				addEdge("built-from", comp, img, 1, sub, "")
				bt := addNode("built", fmt.Sprintf("reg.test/org/r%d@sha256:%d", r, r), "project:p"+fmt.Sprint(r%50))
				addEdge("instance-of", img, bt, 1, sub, "")
				dep := addNode("deployment", fmt.Sprintf("prod/p%d/s%d", r%50, r), "project:p"+fmt.Sprint(r%50))
				addEdge("delivered-to", bt, dep, 0, "project:p"+fmt.Sprint(r%50), "d")
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if s.DB.Postgres() {
		_, _ = s.DB.Exec(bg, `SELECT setval(pg_get_serial_sequence('graph_nodes', 'nid'), (SELECT max(nid) FROM graph_nodes))`)
		// A freshly loaded table has no statistics until autovacuum looks at it.
		_, _ = s.DB.Exec(bg, `ANALYZE graph_nodes`)
		_, _ = s.DB.Exec(bg, `ANALYZE graph_edges`)
	}
	return nodes, edges
}

func TestPerformanceOfTheQueriesOnALargeGraph(t *testing.T) {
	repos, layers := 600, 12 // about 6k nodes and 25k edges: every run
	if os.Getenv("LAUNCHWAY_GRAPH_PERF") != "" {
		repos, layers = 5000, 40 // about 50k nodes and 200k edges
	}
	s := newStore(t)
	t0 := time.Now()
	n, e := synthetic(t, s, repos, layers)
	t.Logf("loaded %d nodes, %d edges in %v", n, e, time.Since(t0).Round(time.Millisecond))
	timed := func(name string, f func()) time.Duration {
		t0 := time.Now()
		f()
		d := time.Since(t0)
		t.Logf("%-46s %v", name, d.Round(time.Millisecond))
		return d
	}
	var im ImpactResult
	d := timed("impact of a bottom-layer package (depth 64)", func() {
		var err error
		im, err = s.Impact(bg, AllowAll{}, ref(KindPackage, "go:example.test/r0/c0"), Limits{Depth: 64, Nodes: 50000, Time: 60000}, When{})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("  reached %d nodes, %d deployments, truncated %v %s", im.Total, len(im.Deployments), im.Truncated, im.Reason)
	if d > testlib.Slow(30*time.Second) {
		t.Fatalf("impact took %v", d)
	}
	timed("impact, default limits", func() {
		r, err := s.Impact(bg, AllowAll{}, ref(KindPackage, "go:example.test/r0/c0"), Limits{}, When{})
		if err != nil || !r.Truncated && r.Total > 2000 {
			t.Fatalf("%+v %v", r.Truncation, err)
		}
	})
	timed("impact as a viewer who reads half the repositories", func() {
		half := Only{}
		for i := 0; i < repos; i += 2 {
			half[fmt.Sprintf("repo:org/r%d", i)] = true
		}
		for i := 0; i < 50; i++ {
			half[fmt.Sprintf("project:p%d", i)] = true
		}
		if _, err := s.Impact(bg, half, ref(KindPackage, "go:example.test/r0/c0"), Limits{Depth: 64, Nodes: 50000, Time: 60000}, When{}); err != nil {
			t.Fatal(err)
		}
	})
	var ord OrderResult
	timed("dependency order of every repository", func() {
		var err error
		ord, err = s.Order(bg, AllowAll{}, OrderQuery{Limits: Limits{Nodes: 50000}})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("  %d repositories in %d levels, %d dependency edges, %d cycles", len(ord.Order), len(ord.Levels), len(ord.Edges), len(ord.Cycles))
	timed("what must rebuild after a push to the first repository", func() {
		rb, err := s.Rebuild(bg, AllowAll{}, "org/r0", Limits{Depth: 64, Nodes: 50000}, When{})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("  %d repositories to rebuild", len(rb.Items))
	})
	timed("path between two nodes", func() {
		if _, err := s.Path(bg, AllowAll{}, ref(KindPackage, "go:example.test/r0/c0"), ref(KindDeployment, fmt.Sprintf("prod/p%d/s%d", (repos-1)%50, repos-1)), Limits{Depth: 64, Nodes: 50000}, When{}); err != nil {
			t.Fatal(err)
		}
	})
	timed("search", func() {
		if _, _, err := s.Search(bg, AllowAll{}, "r12", nil, 50); err != nil {
			t.Fatal(err)
		}
	})
	// The time limit is honest.
	r, err := s.Neighbours(bg, AllowAll{}, ref(KindPackage, "go:example.test/r0/c0"), Downstream, Limits{Depth: 64, Nodes: 50000, Time: 1}, When{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Truncated {
		t.Logf("the one-millisecond limit was not reached (the query finished first)")
	} else if r.Reason != "time" && r.Reason != "nodes" {
		t.Fatalf("%+v", r.Truncation)
	}
}
