package engine

import (
	"errors"
	"testing"
)

// A run restores the default branch's entries but ends only those of the
// scope it saves to. A pull-request run that could discard a default entry
// could empty the cache every other run uses.
func TestCacheDiscardIsLimitedToTheRunsOwnScope(t *testing.T) {
	r := newCacheRig(t)
	k := cacheKey("lock")
	main := r.start("demo", cacheOnMain)
	r.mustSave(main, "pip", k, []byte("from main"))
	ok(t, r.e, main, nil)

	pr := r.start("demo", cacheOnPull(7))
	def := r.lookup(pr, "pip", k, "")
	if def == nil || def.Scope != "default" {
		t.Fatalf("the pull request should restore main's entry: %+v", def)
	}
	if err := r.e.DiscardCache(pr.LeaseID, def.ID, "it looked damaged"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a pull-request run discarding a default entry: %v", err)
	}
	if got := r.entries("demo"); len(got) != 1 {
		t.Fatalf("the default entry must still be kept: %v", got)
	}
	// Its own entry it may discard.
	r.mustSave(pr, "pip", cacheKey("own"), []byte("from the pull request"))
	var own CacheEntryView
	for _, v := range r.entries("demo") {
		if v.Scope == "pull:7" {
			own = v
		}
	}
	if own.ID == "" {
		t.Fatal("no entry of the pull request")
	}
	if err := r.e.DiscardCache(pr.LeaseID, own.ID, "damaged"); err != nil {
		t.Fatalf("discarding its own entry: %v", err)
	}
	ok(t, r.e, pr, nil)

	// A run of the default branch ends a default entry.
	main2 := r.start("demo", cacheOnMain)
	if err := r.e.DiscardCache(main2.LeaseID, def.ID, "damaged"); err != nil {
		t.Fatalf("a default-branch run discarding a default entry: %v", err)
	}
	if got := r.entries("demo"); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
