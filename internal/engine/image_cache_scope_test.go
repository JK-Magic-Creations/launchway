package engine

import (
	"strings"
	"testing"
)

// Whose registry cache an image step writes follows how the run started: the
// protected cache for a default-branch run, a tag of its own for any other.
func TestImageCacheScopeFollowsTheTrustScope(t *testing.T) {
	e := &Engine{}
	e.SetDefaultBranch(func(string) string { return "main" })
	st := func(tr Trigger) *RunState { return &RunState{Project: "p", Trigger: tr} }
	if s, w, np := e.imageCacheScope(st(cacheOnMain)); s != "" || !w || np {
		t.Fatalf("default branch: %q %v %v", s, w, np)
	}
	s, w, _ := e.imageCacheScope(st(cacheOnBranch))
	if !strings.HasPrefix(s, "ref-") || !w {
		t.Fatalf("branch: %q %v", s, w)
	}
	if s2, _, _ := e.imageCacheScope(st(Trigger{Event: "push", Ref: "refs/heads/other", Branch: "other"})); s2 == s || s2 == "" {
		t.Fatalf("two branches share a cache scope: %q %q", s, s2)
	}
	if s, w, _ := e.imageCacheScope(st(cacheOnPull(7))); s != "pull-7" || w {
		t.Fatalf("a pull request that may not read secrets may write: %q %v", s, w)
	}
	fork := cacheOnPull(8)
	fork.Fork = true
	if s, _, np := e.imageCacheScope(st(fork)); s != "pull-8" || !np {
		t.Fatalf("fork: %q %v", s, np)
	}
	if s, w, _ := e.imageCacheScope(&RunState{Project: "p", Trigger: cacheOnMain, Draft: &Draft{}}); s != "" || w {
		t.Fatalf("a draft writes a cache: %q %v", s, w)
	}
}
