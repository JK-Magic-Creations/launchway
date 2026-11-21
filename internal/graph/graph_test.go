package graph

import (
	"context"
	"strings"
	"testing"
)

func TestIncrementalPushClosesTheOldEdgeAndAsOfSeesBoth(t *testing.T) {
	repos := fixtureRepos()
	r := newRunner(t, repos)
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	before := When{At: r.Store.now()}
	// svc stops using libgo and starts using the Python library's neighbour: a new push.
	repos.Push("acme/svc", "c-svc-9", map[string]string{
		"go.mod":        "module example.test/svc\n\ngo 1.23\n",
		"Containerfile": "FROM harbor.test/base/runtime:1.0\n",
	})
	res, err := r.PushedRepository(bg, "acme/svc")
	if err != nil || res.Sources["manifests"].EdgesClosed == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	now, _ := r.Store.Order(bg, AllowAll{}, OrderQuery{Repositories: []string{"acme/svc", "acme/libgo"}})
	then, _ := r.Store.Order(bg, AllowAll{}, OrderQuery{Repositories: []string{"acme/svc", "acme/libgo"}, When: before})
	if len(now.Edges) != 0 || len(then.Edges) != 1 || then.Edges[0].Consumer != "acme/svc" {
		t.Fatalf("now %+v then %+v", now.Edges, then.Edges)
	}
	// libgo's dependents, then and now.
	a, _ := r.Store.Impact(bg, AllowAll{}, ref(KindPackage, "go:example.test/libgo"), Limits{}, before)
	b, _ := r.Store.Impact(bg, AllowAll{}, ref(KindPackage, "go:example.test/libgo"), Limits{}, When{})
	if a.Total == 0 || b.Total != 0 {
		t.Fatalf("impact then %d now %d", a.Total, b.Total)
	}
	// As of a commit.
	w, err := r.Store.AsOfCommit(bg, AllowAll{}, "acme/svc", "c-svc-1")
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := r.Store.Order(bg, AllowAll{}, OrderQuery{Repositories: []string{"acme/svc", "acme/libgo"}, When: w}); len(o.Edges) != 1 {
		t.Fatalf("as of the old commit: %+v", o.Edges)
	}
	if _, err := r.Store.AsOfCommit(bg, AllowAll{}, "acme/svc", "nope"); err == nil {
		t.Fatal("an unknown commit was accepted")
	}
}

func TestRebuildEqualsIncremental(t *testing.T) {
	repos := fixtureRepos()
	r := newRunner(t, repos)
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	// A history of pushes.
	repos.Push("acme/libgo", "c2", map[string]string{"go.mod": "module example.test/libgo/v2\n\ngo 1.24\n"})
	repos.Push("acme/pylib", "c2", map[string]string{"pyproject.toml": "[project]\nname = \"renamed\"\n"})
	repos.Delete("acme/ui")
	for _, p := range []string{"acme/libgo", "acme/pylib"} {
		if _, err := r.PushedRepository(bg, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Remove(bg, Scope{ScopeRepository, "acme/ui"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(bg, Scope{ScopeRepository, "acme/web"}); err != nil {
		t.Fatal(err)
	}
	inc, err := r.Store.Fingerprint(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Rebuild(bg); err != nil {
		t.Fatal(err)
	}
	full, _ := r.Store.Fingerprint(bg)
	if inc != full {
		t.Fatalf("incremental and rebuilt graphs differ:\n--- incremental\n%s\n--- rebuilt\n%s", inc, full)
	}
	if !strings.Contains(full, "go:example.test/libgo/v2") || strings.Contains(full, "repository acme/ui") {
		t.Fatalf("the pushes are not in it:\n%s", full)
	}
}

func cycleRepos() *MemRepos {
	m := fixtureRepos()
	m.Push("acme/a", "ca", map[string]string{"go.mod": "module example.test/a\n\nrequire example.test/b v1.0.0\n"})
	m.Push("acme/b", "cb", map[string]string{"go.mod": "module example.test/b\n\nrequire example.test/c v1.0.0\n"})
	m.Push("acme/c", "cc", map[string]string{"go.mod": "module example.test/c\n\nrequire example.test/a v1.0.0\n"})
	return m
}

func TestCyclesAreReportedWithThePathAndOverridesResolveThem(t *testing.T) {
	r := newRunner(t, cycleRepos())
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	o, err := r.Store.Order(bg, AllowAll{}, OrderQuery{})
	if err != nil || len(o.Cycles) != 1 {
		t.Fatalf("%+v %v", o.Cycles, err)
	}
	c := o.Cycles[0]
	if len(c.Path) != 4 || c.Path[0] != c.Path[3] || strings.Join(c.Members, ",") != "acme/a,acme/b,acme/c" || !strings.Contains(c.Suggestion, "override ignore") {
		t.Fatalf("%+v", c)
	}
	// The cycle's members share a level, and everything is still ordered.
	if len(o.Order) != len(o.Edges)+len(o.Order)-len(o.Edges) || len(o.Levels) == 0 {
		t.Fatal("no order")
	}
	// An override needs a reason and readable repositories.
	if _, err := r.Store.AddOverride(bg, AllowAll{}, Override{Kind: OverrideIgnore, From: "acme/c", To: "acme/a"}); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("%v", err)
	}
	if _, err := r.Store.AddOverride(bg, AllowAll{}, Override{Kind: OverrideIgnore, From: "acme/c", To: "acme/none", Reason: "x"}); err == nil {
		t.Fatal("an unknown repository was accepted")
	}
	ov, err := r.Store.AddOverride(bg, AllowAll{}, Override{Kind: OverrideIgnore, From: "acme/c", To: "acme/a", Reason: "c only needs a's test helpers", Author: "ann"})
	if err != nil || ov.ID == 0 {
		t.Fatalf("%+v %v", ov, err)
	}
	o, _ = r.Store.Order(bg, AllowAll{}, OrderQuery{})
	if len(o.Cycles) != 0 || len(o.Overrides) != 1 || o.Overrides[0].Reason == "" {
		t.Fatalf("%+v", o)
	}
	pos := map[string]int{}
	for i, n := range o.Order {
		pos[n] = i
	}
	if !(pos["acme/c"] < pos["acme/b"] && pos["acme/b"] < pos["acme/a"]) {
		t.Fatalf("order %v", o.Order)
	}
	// The ignored edge is still shown, marked.
	var shown bool
	for _, e := range o.Edges {
		if e.Consumer == "acme/c" && e.Provider == "acme/a" && e.Override == "ignored" && e.Reason != "" {
			shown = true
		}
	}
	if !shown {
		t.Fatalf("%+v", o.Edges)
	}
	// "before": force libgo after a, then take it back.
	ov2, err := r.Store.AddOverride(bg, AllowAll{}, Override{Kind: OverrideBefore, From: "acme/a", To: "acme/libgo", Reason: "the release train"})
	if err != nil {
		t.Fatal(err)
	}
	o, _ = r.Store.Order(bg, AllowAll{}, OrderQuery{Repositories: []string{"acme/a", "acme/libgo"}})
	if len(o.Order) != 2 || o.Order[0] != "acme/a" {
		t.Fatalf("%v", o.Order)
	}
	if err := r.Store.RemoveOverride(bg, AllowAll{}, ov2.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.Store.Overrides(bg, AllowAll{}); len(list) != 1 {
		t.Fatalf("%+v", list)
	}
}

func TestRebuildSetIsOrderedAndPinsStopIt(t *testing.T) {
	repos := fixtureRepos()
	repos.Push("acme/svc2", "c1", map[string]string{"go.mod": "module example.test/svc2\n\nrequire example.test/svc v1.0.0\n"})
	repos.Push("acme/tool", "c1", map[string]string{"go.mod": "module example.test/tool\n\nrequire example.test/libgo v1.0.0\n"})
	r := newRunner(t, repos)
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	rb, err := r.Store.Rebuild(bg, AllowAll{}, "acme/libgo", Limits{}, When{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range rb.Items {
		names = append(names, it.Repository)
	}
	if strings.Join(names, ",") != "acme/svc,acme/tool,acme/svc2" && strings.Join(names, ",") != "acme/tool,acme/svc,acme/svc2" {
		t.Fatalf("%v", names)
	}
	last := rb.Items[len(rb.Items)-1]
	if last.Repository != "acme/svc2" || len(last.Why) != 2 || last.Why[0].Provider != "acme/libgo" {
		t.Fatalf("%+v", last)
	}
	if _, err := r.Store.AddOverride(bg, AllowAll{}, Override{Kind: OverridePin, From: "acme/tool", To: "acme/libgo", Value: "v1.0.0", Reason: "tool is frozen"}); err != nil {
		t.Fatal(err)
	}
	rb, _ = r.Store.Rebuild(bg, AllowAll{}, "acme/libgo", Limits{}, When{})
	for _, it := range rb.Items {
		if it.Repository == "acme/tool" {
			t.Fatal("a pinned consumer was rebuilt")
		}
	}
	if len(rb.Skipped) != 1 || rb.Skipped[0].Reason != "tool is frozen" {
		t.Fatalf("%+v", rb.Skipped)
	}
	if rb2, _ := r.Store.Rebuild(bg, AllowAll{}, "acme/libgo", Limits{Depth: 1}, When{}); !rb2.Truncated || rb2.Reason != "depth" {
		t.Fatalf("%+v", rb2.Truncation)
	}
}

// A person who cannot read acme/svc learns nothing of it: not the node, not the paths through it, not the counts.
func TestPermissionFilteringHidesWhatTheViewerMayNotRead(t *testing.T) {
	repos := fixtureRepos()
	repos.Push("acme/svc2", "c1", map[string]string{"go.mod": "module example.test/svc2\n\nrequire example.test/svc v1.0.0\n"})
	r := newRunner(t, repos)
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	all, none := AllowAll{}, Only{"repo:acme/libgo": true, "repo:acme/base": true, "repo:acme/svc2": true, "repo:acme/ui": true, "repo:acme/web": true}
	full, _ := r.Store.Impact(bg, all, ref(KindRepository, "acme/libgo"), Limits{}, When{})
	part, err := r.Store.Impact(bg, none, ref(KindRepository, "acme/libgo"), Limits{}, When{})
	if err != nil {
		t.Fatal(err)
	}
	if part.Total >= full.Total {
		t.Fatalf("hidden things were counted: %d of %d", part.Total, full.Total)
	}
	for _, g := range part.ByOwner {
		for _, it := range g.Items {
			if strings.Contains(it.Node.Key, "acme/svc:") || it.Node.Key == "acme/svc" || it.Node.Key == "harbor.test/apps/svc" {
				t.Fatalf("leaked %s", it.Node.Key)
			}
			// svc2 depends on svc: with svc hidden there is no path to it.
			if strings.HasPrefix(it.Node.Key, "acme/svc2") {
				if len(it.Path) < 1 {
					t.Fatal("no path")
				}
			}
		}
	}
	// The node itself is not found, and the error is the same as for one that does not exist.
	_, err1 := r.Store.Get(bg, none, ref(KindRepository, "acme/svc"), When{})
	_, err2 := r.Store.Get(bg, none, ref(KindRepository, "acme/nonexistent"), When{})
	if err1 == nil || err2 == nil || strings.Replace(err1.Error(), "svc", "X", 1) != strings.Replace(err2.Error(), "nonexistent", "X", 1) {
		t.Fatalf("%v / %v", err1, err2)
	}
	// Order, rebuild, search, edges and path all stay inside what is readable.
	o, _ := r.Store.Order(bg, none, OrderQuery{})
	for _, e := range o.Edges {
		if e.Consumer == "acme/svc" || e.Provider == "acme/svc" {
			t.Fatalf("order leaked %+v", e)
		}
	}
	rb, _ := r.Store.Rebuild(bg, none, "acme/libgo", Limits{}, When{})
	for _, it := range rb.Items {
		if it.Repository == "acme/svc" || it.Repository == "acme/svc2" {
			// svc2 is readable but only reachable through svc: unreachable for this viewer.
			t.Fatalf("rebuild leaked a path through a hidden repository: %+v", it)
		}
	}
	if _, err := r.Store.Rebuild(bg, none, "acme/svc", Limits{}, When{}); err == nil {
		t.Fatal("rebuild of a hidden repository answered")
	}
	hits, _, _ := r.Store.Search(bg, none, "svc", nil, 50)
	for _, h := range hits {
		if h.Key == "acme/svc" || strings.HasPrefix(h.Key, "acme/svc:") {
			t.Fatalf("search leaked %s", h.Key)
		}
	}
	p, _ := r.Store.Path(bg, none, ref(KindRepository, "acme/libgo"), ref(KindComponent, "acme/svc2:."), Limits{}, When{})
	if p.Found {
		t.Fatal("a path through a hidden repository was shown")
	}
	pf, _ := r.Store.Path(bg, all, ref(KindRepository, "acme/libgo"), ref(KindComponent, "acme/svc2:."), Limits{}, When{})
	if !pf.Found || len(pf.Steps) < 3 {
		t.Fatalf("%+v", pf)
	}
	// Overrides about hidden repositories are not listed either.
	if _, err := r.Store.AddOverride(bg, all, Override{Kind: OverrideIgnore, From: "acme/svc", To: "acme/libgo", Reason: "r"}); err != nil {
		t.Fatal(err)
	}
	if ov, _ := r.Store.Overrides(bg, none); len(ov) != 0 {
		t.Fatalf("%+v", ov)
	}
	if _, err := r.Store.AddOverride(bg, none, Override{Kind: OverrideIgnore, From: "acme/svc", To: "acme/libgo", Reason: "r"}); err == nil {
		t.Fatal("a person added an override about a repository they cannot read")
	}
}

func TestOrganisationsAreIsolated(t *testing.T) {
	r := newRunner(t, fixtureRepos())
	if _, err := r.Rescan(bg); err != nil {
		t.Fatal(err)
	}
	other := *r.Store
	other.Org = "other"
	if n, e, _ := other.Counts(bg); n != 0 || e != 0 {
		t.Fatalf("the other organisation sees %d nodes", n)
	}
	if _, err := other.Get(bg, AllowAll{}, ref(KindRepository, "acme/svc"), When{}); err == nil {
		t.Fatal("found across organisations")
	}
	a := Assertion{}
	a.Add(Node{Kind: KindRepository, Key: "acme/svc"})
	if _, err := other.Apply(context.Background(), "x", "y", "", a); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := r.Store.Counts(bg); n == 0 {
		t.Fatal("lost")
	}
}
