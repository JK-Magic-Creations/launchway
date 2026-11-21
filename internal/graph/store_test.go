package graph

import (
	"strings"
	"testing"
)

func TestApplyIsIdempotentAndKeepsHistory(t *testing.T) {
	s := newStore(t)
	a := Assertion{}
	a.Add(repoN("team/app"))
	a.Link(Edge{Kind: EdgeDependsOn, From: ref(KindComponent, "team/app:."), To: ref(KindPackage, "go:example.com/lib"), Constraint: "v1.2.0", Resolved: "v1.2.0", Subject: RepoSubject("team/app")})
	r, err := s.Apply(bg, "manifests", "repository:team/app", "c1", a)
	if err != nil || r.EdgesNew != 1 || r.NodesNew != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = s.Apply(bg, "manifests", "repository:team/app", "c1", a)
	if err != nil || r.Changed() || r.EdgesUnchanged != 1 {
		t.Fatalf("a second identical assertion changed something: %+v %v", r, err)
	}
	// The resolved version moves: the old edge closes and a new one opens.
	a.Edges[0].Resolved, a.Edges[0].Constraint = "v1.3.0", "v1.3.0"
	if r, err = s.Apply(bg, "manifests", "repository:team/app", "c2", a); err != nil || r.EdgesChanged != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	var cur, closed int
	_ = s.DB.QueryRow(bg, `SELECT count(*) FROM graph_edges WHERE valid_to = 0`).Scan(&cur)
	_ = s.DB.QueryRow(bg, `SELECT count(*) FROM graph_edges WHERE valid_to <> 0`).Scan(&closed)
	if cur != 1 || closed != 1 {
		t.Fatalf("current %d closed %d", cur, closed)
	}
	// The dependency goes away: closed, not deleted.
	a.Edges = nil
	if r, err = s.Apply(bg, "manifests", "repository:team/app", "c3", a); err != nil || r.EdgesClosed != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	_ = s.DB.QueryRow(bg, `SELECT count(*) FROM graph_edges`).Scan(&cur)
	if cur != 2 {
		t.Fatalf("history lost: %d rows", cur)
	}
}

func TestApplyRefusesBadInput(t *testing.T) {
	s := newStore(t)
	if _, err := s.Apply(bg, "", "x", "", Assertion{}); err == nil {
		t.Fatal("no source accepted")
	}
	a := Assertion{}
	a.Link(Edge{Kind: "nonsense", From: ref(KindRepository, "a"), To: ref(KindRepository, "b")})
	if _, err := s.Apply(bg, "m", "x", "", a); err == nil || !strings.Contains(err.Error(), "unknown edge kind") {
		t.Fatalf("%v", err)
	}
}

// chain builds a-1 -> a-2 ... where each depends on the next, in one scope.
func chain(t testing.TB, s *Store, n int) {
	t.Helper()
	a := Assertion{}
	for i := 0; i < n; i++ {
		a.Link(Edge{Kind: EdgeDependsOn, From: ref(KindPackage, key(i)), To: ref(KindPackage, key(i+1))})
	}
	if _, err := s.Apply(bg, "t", "chain", "", a); err != nil {
		t.Fatal(err)
	}
}

func key(i int) string { return "go:m" + string(rune('a'+i%26)) + string(rune('a'+i/26)) }

func TestWalkIsBoundedAndSaysSo(t *testing.T) {
	s := newStore(t)
	chain(t, s, 12)
	// What is affected if the far end changes: everything upstream of it, 12 hops.
	r, err := s.Neighbours(bg, AllowAll{}, ref(KindPackage, key(12)), Downstream, Limits{Depth: 5}, When{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Hits) != 5 || !r.Truncated || r.Reason != "depth" {
		t.Fatalf("%d hits, %+v", len(r.Hits), r.Truncation)
	}
	r, _ = s.Neighbours(bg, AllowAll{}, ref(KindPackage, key(12)), Downstream, Limits{Depth: 20, Nodes: 4}, When{})
	if len(r.Hits) != 3 || !r.Truncated || r.Reason != "nodes" {
		t.Fatalf("%d hits, %+v", len(r.Hits), r.Truncation)
	}
	r, _ = s.Neighbours(bg, AllowAll{}, ref(KindPackage, key(12)), Downstream, Limits{}, When{})
	if len(r.Hits) != 8 || !r.Truncated {
		t.Fatalf("default depth: %d hits, %+v", len(r.Hits), r.Truncation)
	}
	last := r.Hits[len(r.Hits)-1]
	if len(last.Path) != 8 || last.Path[0].Edge.From.Key != key(11) || last.Path[0].Edge.To.Key != key(12) {
		t.Fatalf("the path says why: %+v", last.Path[0])
	}
	// Cycles end.
	a := Assertion{}
	a.Link(Edge{Kind: EdgeDependsOn, From: ref(KindPackage, key(12)), To: ref(KindPackage, key(0))})
	if _, err := s.Apply(bg, "t", "loop", "", a); err != nil {
		t.Fatal(err)
	}
	r, err = s.Neighbours(bg, AllowAll{}, ref(KindPackage, key(5)), Downstream, Limits{Depth: 64}, When{})
	if err != nil || len(r.Hits) != 12 || r.Truncated {
		t.Fatalf("%d %+v %v", len(r.Hits), r.Truncation, err)
	}
}
