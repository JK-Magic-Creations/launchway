package engine

import (
	"context"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// saveFrom saves an entry that started from base (an entry id, or "").
func (r *cacheRig) saveFrom(j *Job, name, key, base string, data []byte) (*BegunCache, *CommittedCache, error) {
	r.t.Helper()
	ctx := context.Background()
	begun, err := r.e.BeginCache(ctx, j.LeaseID, BeginCache{Name: name, Key: key, Platform: linux, Bytes: int64(len(data)), Files: 1, SHA256: sum(data), Base: base, Content: sum([]byte("c" + string(data)))})
	if err != nil || begun.Exists {
		return begun, nil, err
	}
	got, err := r.e.PutCacheChunk(ctx, j.LeaseID, begun.ID, 0, sum(data), data)
	if err != nil || got != int64(len(data)) {
		r.t.Fatalf("%d %v", got, err)
	}
	c, err := r.e.CommitCache(ctx, j.LeaseID, begun.ID)
	return begun, c, err
}

func (r *cacheRig) lookupFull(j *Job, name, key, fallback string) *CacheLookupResult {
	r.t.Helper()
	res, err := r.e.LookupCache(j.LeaseID, CacheLookup{Name: name, Key: key, Platform: linux, Fallback: fallback})
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

// A key that already has an entry is saved again only as the next
// generation, by a step that started from it; a restore takes the newest, and
// the older generation ends.
func TestAKeyIsSavedAgainAsTheNextGeneration(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	key := cacheKey("lock")
	b1, _, err := r.saveFrom(j, "pip", key, "", []byte("one"))
	if err != nil || b1.Generation != 1 {
		t.Fatalf("%+v %v", b1, err)
	}
	// A step that did not start from it may not replace it, nor may a stale base.
	if b, _, err := r.saveFrom(j, "pip", key, "", []byte("two")); err != nil || !b.Exists {
		t.Fatalf("an entry that was not the step's base was replaced: %+v %v", b, err)
	}
	en := r.lookup(j, "pip", key, "")
	if en == nil || en.Generation != 1 || en.Content == "" {
		t.Fatalf("%+v", en)
	}
	b2, c2, err := r.saveFrom(j, "pip", key, en.ID, []byte("two"))
	if err != nil || b2.Generation != 2 || c2 == nil {
		t.Fatalf("%+v %v", b2, err)
	}
	en2 := r.lookup(j, "pip", key, "")
	if en2 == nil || en2.Generation != 2 || en2.ID != c2.ID {
		t.Fatalf("a restore did not take the newest generation: %+v", en2)
	}
	if data, err := r.download(j, en2.ID); err != nil || string(data) != "two" {
		t.Fatalf("%q %v", data, err)
	}
	if have := r.entries("demo"); len(have) != 1 {
		t.Fatalf("the older generation was kept: %v", have)
	}
	// Two steps that both started from generation 2 cannot both save 3: the
	// second is told it exists.
	if _, _, err := r.saveFrom(j, "pip", key, en2.ID, []byte("three")); err != nil {
		t.Fatal(err)
	}
	if b, _, err := r.saveFrom(j, "pip", key, en2.ID, []byte("three too")); err != nil || !b.Exists {
		t.Fatalf("%+v %v", b, err)
	}
}

// An entry older than the maximum age (from when its contents were started
// from scratch) is not restored, exactly or as a fallback; the step is told,
// and saves the key again as a newer generation that starts a new life.
func TestAnEntryPastItsMaximumAgeIsNotRestored(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	key := cacheKey("lock")
	if _, _, err := r.saveFrom(j, "pip", key, "", []byte("old")); err != nil {
		t.Fatal(err)
	}
	r.advance(6 * 24 * time.Hour)
	if res := r.lookupFull(j, "pip", key, "latest"); res.Entry == nil || res.Entry.AgeSeconds < 6*24*3600 || res.MaxAgeSeconds != 7*24*3600 {
		t.Fatalf("%+v", res)
	}
	r.advance(2 * 24 * time.Hour)
	res := r.lookupFull(j, "pip", key, "latest")
	if res.Entry != nil || res.Expired == nil || res.Expired.Generation != 1 {
		t.Fatalf("an eight-day-old entry was restored under a seven-day maximum: %+v", res)
	}
	if other := r.lookupFull(j, "pip", cacheKey("other"), "latest"); other.Entry != nil {
		t.Fatalf("an expired entry was restored as a fallback: %+v", other)
	}
	_, c, err := r.saveFrom(j, "pip", key, res.Expired.ID, []byte("rebuilt"))
	if err != nil || c == nil {
		t.Fatal(err)
	}
	fresh := r.lookupFull(j, "pip", key, "")
	if fresh.Entry == nil || fresh.Entry.Generation != 2 || fresh.Entry.AgeSeconds > 60 {
		t.Fatalf("the rebuilt entry did not start a new life: %+v", fresh)
	}
}

// A cache may ask for a shorter maximum age than the policy's, never a longer.
func TestACachesOwnMaxAgeCanOnlyShortenThePolicys(t *testing.T) {
	r := newCacheRig(t)
	key := cacheKey("lock")
	short := func(c *pipeline.Cache) { c.MaxAge = pipeline.Duration(2 * time.Hour) }
	long := func(c *pipeline.Cache) { c.MaxAge = pipeline.Duration(300 * 24 * time.Hour) }
	j := r.startPipeline("shop", pipelineWith(t, "ci", short), cacheOnMain)
	if _, _, err := r.saveFrom(j, "deps", key, "", []byte("x")); err != nil {
		t.Fatal(err)
	}
	r.finish(j)
	r.advance(3 * time.Hour)
	j = r.startPipeline("shop", pipelineWith(t, "ci", short), cacheOnMain)
	if res := r.lookupFull(j, "deps", key, ""); res.Entry != nil || res.Expired == nil {
		t.Fatalf("%+v", res)
	}
	r.finish(j)
	j = r.startPipeline("shop", pipelineWith(t, "ci", long), cacheOnMain)
	if res := r.lookupFull(j, "deps", key, ""); res.Entry == nil || res.MaxAgeSeconds != 7*24*3600 {
		t.Fatalf("a longer max_age than the policy's was honored: %+v", res)
	}
}

// What a step saves after restoring something old keeps its age: a chain of
// fallbacks, each a superset of the last, ends after the maximum age instead
// of living for ever.
func TestASaveKeepsTheAgeOfWhatItStartedFrom(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	first := cacheKey("first")
	if _, _, err := r.saveFrom(j, "pip", first, "", []byte("a")); err != nil {
		t.Fatal(err)
	}
	r.advance(5 * 24 * time.Hour)
	en := r.lookup(j, "pip", cacheKey("second"), "latest")
	if en == nil || en.Exact {
		t.Fatalf("%+v", en)
	}
	second := cacheKey("second")
	if _, _, err := r.saveFrom(j, "pip", second, en.ID, []byte("a+b")); err != nil {
		t.Fatal(err)
	}
	r.advance(3 * 24 * time.Hour)
	if res := r.lookupFull(j, "pip", second, "latest"); res.Entry != nil || res.Expired == nil {
		t.Fatalf("the superset lived on past the maximum age of what it started from: %+v", res)
	}
	// A save that started from nothing is born now.
	if _, _, err := r.saveFrom(j, "pip", cacheKey("third"), "", []byte("c")); err != nil {
		t.Fatal(err)
	}
	r.advance(6 * 24 * time.Hour)
	if res := r.lookupFull(j, "pip", cacheKey("third"), ""); res.Entry == nil {
		t.Fatalf("%+v", res)
	}
}

// Restoring an entry as a fallback does not keep it from falling idle; an
// exact restore does.
func TestAFallbackRestoreDoesNotKeepAnEntryAlive(t *testing.T) {
	r := newCacheRig(t)
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 7, MaxAgeDays: 90})
	j := r.start("demo", cacheOnMain)
	if _, _, err := r.saveFrom(j, "pip", cacheKey("a"), "", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.saveFrom(j, "models", cacheKey("b"), "", []byte("b")); err != nil {
		t.Fatal(err)
	}
	r.advance(6 * 24 * time.Hour)
	if en := r.lookup(j, "pip", cacheKey("elsewhere"), "latest"); en == nil || en.Exact {
		t.Fatalf("%+v", en)
	}
	if en := r.lookup(j, "models", cacheKey("b"), ""); en == nil || !en.Exact {
		t.Fatalf("%+v", en)
	}
	r.advance(2 * 24 * time.Hour)
	r.e.pruneCaches(context.Background())
	have := r.entries("demo")
	if len(have) != 1 {
		t.Fatalf("%v", have)
	}
	for _, v := range have {
		if v.Name != "models" {
			t.Fatalf("the entry that was only restored as a fallback is still kept: %+v", v)
		}
	}
}

// The pruner ends entries older than the maximum age even if they are used.
func TestEntriesPastTheMaximumAgeEnd(t *testing.T) {
	r := newCacheRig(t)
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 30, MaxAgeDays: 3})
	j := r.start("demo", cacheOnMain)
	if _, _, err := r.saveFrom(j, "pip", cacheKey("a"), "", []byte("a")); err != nil {
		t.Fatal(err)
	}
	r.advance(2 * 24 * time.Hour)
	r.lookup(j, "pip", cacheKey("a"), "")
	r.advance(2 * 24 * time.Hour)
	r.e.pruneCaches(context.Background())
	if have := r.entries("demo"); len(have) != 0 {
		t.Fatalf("%v", have)
	}
}
