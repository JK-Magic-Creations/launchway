package engine

import "testing"

// When the project's default branch is not known, no run writes the default
// scope: a run of "main" may or may not be of the default branch, and the
// default scope is what every other run restores.
func TestCacheNoDefaultScopeWithoutAKnownDefaultBranch(t *testing.T) {
	r := newCacheRig(t)
	for name, fn := range map[string]func(string) string{
		"nothing set":            nil,
		"the project is unknown": func(string) string { return "" },
	} {
		r.e.caches.defaultBranch.Store(nil)
		if fn != nil {
			r.e.SetDefaultBranch(fn)
		}
		for _, trig := range []Trigger{cacheOnMain, {Event: "push", Branch: "master", Ref: "refs/heads/master"}} {
			s := r.e.scopesOf(&RunState{Project: "demo", Trigger: trig})
			if s.write == "default" {
				t.Errorf("%s: a push to %s writes the default scope", name, trig.Branch)
			}
			if want := "ref:" + trig.Ref; s.write != want {
				t.Errorf("%s: a push to %s writes %q, want its own %q", name, trig.Branch, s.write, want)
			}
			if len(s.read) != 2 || s.read[1] != "default" {
				t.Errorf("%s: it still reads the default scope after its own: %v", name, s.read)
			}
		}
	}
	// And through a real run: the save lands in the branch's own scope.
	r.e.caches.defaultBranch.Store(nil)
	j := r.start("demo", cacheOnMain)
	b, err := r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("x"), Platform: linux, Bytes: 3, Files: 1, SHA256: sum([]byte("abc"))})
	if err != nil || b.Scope != "ref:refs/heads/main" {
		t.Fatalf("%+v %v", b, err)
	}
}
