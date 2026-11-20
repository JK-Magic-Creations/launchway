package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const cachedStep = `
pipeline: demo
steps:
  test:
    image: x
    run: pytest
    cache:
      - name: pip
        paths: [.cache/pip]
        key: { files: [requirements.txt], text: v1 }
        fallback: latest
        save: always
      - { name: models, paths: [models], key: { text: m1 } }
`

// A step with a cache is leased only to a runner that says it has caches,
// and a runner that does is told what to do.
func TestCacheStepsGoOnlyToRunnersThatKnowCaches(t *testing.T) {
	e, _ := newStoreEngine(t, ArtifactOptions{})
	id := create(t, e, cachedStep, Trigger{Event: "manual"})
	old := RunnerInfo{ID: "old", Name: "old", Features: FeatureArtifacts + " " + FeatureCoverage}
	if j, err := e.Lease(context.Background(), old, 0); err != nil || j != nil {
		t.Fatalf("a runner without the feature was given %+v %v", j, err)
	}
	j := take(t, e)
	if j.RunID != id || j.Cache == nil || len(j.Cache.Caches) != 2 || j.Cache.ChunkBytes != ArtifactChunkBytes || j.Cache.MaxEntryBytes != 2<<30 || j.Cache.Problem != "" {
		t.Fatalf("%+v %+v", j, j.Cache)
	}
	c := j.Cache.Caches[0]
	if c.Name != "pip" || c.Paths[0] != ".cache/pip" || c.KeyFiles[0] != "requirements.txt" || c.KeyText != "v1" || c.Fallback != "latest" || c.Save != "always" {
		t.Fatalf("%+v", c)
	}
	// What the project's policy says about an entry is told to the runner.
	if _, _, err := e.SetCachePolicy("demo", CachePolicy{Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 0.5, IdleDays: 3}, "ann"); err != nil {
		t.Fatal(err)
	}
	ok(t, e, j, nil)
	id2 := create(t, e, cachedStep, Trigger{Event: "manual"})
	j = take(t, e)
	if j.RunID != id2 || j.Cache.MaxEntryBytes != 1<<29 {
		t.Fatalf("%+v", j.Cache)
	}
	// A step without caches carries none, whoever takes it.
	ok(t, e, j, nil)
	create(t, e, linear, Trigger{Event: "manual"})
	if j, err := e.Lease(context.Background(), old, 0); err != nil || j == nil || j.Cache != nil {
		t.Fatalf("%+v %v", j, err)
	}
}

// A run whose pipeline uses a cache needs schema 25: servers that do not
// know it leave it alone, say so, and refuse to make one.
func TestOlderServerLeavesCacheRunsAlone(t *testing.T) {
	n := schemaCache
	if migrations[n-1].compat != additive {
		t.Fatalf("migration %d must be additive", n)
	}
	clk, addr := newClock(), testdb.New(t)
	var oldLog syncBuffer
	cur := mustOpenAs(t, addr, clk, n, &syncBuffer{})
	cached := create(t, cur, cachedStep, Trigger{Event: "manual"})
	plain := create(t, cur, linear, Trigger{Event: "manual"})
	if sum, _ := cur.Summary(cached); sum.MinSchema != n {
		t.Fatalf("a run with a cache has min_schema %d, want %d", sum.MinSchema, n)
	}
	if sum, _ := cur.Summary(plain); sum.MinSchema >= n {
		t.Fatalf("a plain run needs schema %d", sum.MinSchema)
	}
	old := mustOpenAs(t, addr, clk, n-1, &oldLog)
	d, err := old.GetRun(cached)
	if err != nil || !d.NeedsNewerServer || !strings.Contains(strings.Join(d.NotUnderstood, " "), "schema 25") {
		t.Fatalf("the older server's view: %+v %v", d, err)
	}
	if err := old.Cancel(cached, "x"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("an older server cancelled a run it does not understand: %v", err)
	}
	if _, err := old.CreateRun(NewRun{Project: "demo", Pipeline: parse(t, cachedStep), Trigger: Trigger{Event: "manual"}, Source: Source{Kind: "dir", Path: "/x"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("an older server made a run with a cache: %v", err)
	}
	for range 3 {
		j := take(t, old)
		if j.RunID != plain {
			t.Fatalf("an older server leased %s/%s", j.RunID, j.StepID)
		}
		ok(t, old, j, nil)
	}
	none(t, old)
	// The current server finishes it, and an older one's results are intact.
	j := take(t, cur)
	if j.RunID != cached || j.Cache == nil {
		t.Fatalf("%+v", j)
	}
	ok(t, cur, j, nil)
	want(t, statuses(t, cur, cached), "run", "succeeded")
}

// What a runner says its caches did is kept in the event that ends the step,
// as far as it makes sense, and shows in the step's view.
func TestCacheResultsAreKeptAndCleaned(t *testing.T) {
	e, _ := newEngine(t)
	id := create(t, e, cachedStep, Trigger{Event: "manual"})
	j := take(t, e)
	zero := 0
	key := sum([]byte("k"))
	err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Caches: []CacheResult{
		{Name: "pip", Key: key, Result: CacheSaved, Restored: CacheFallback, Bytes: 1234, Scope: "default", Seconds: 4.5},
		{Name: "models", Key: "not a key", Result: CacheNotSaved, Reason: "a pull-request run\nwrites only \x01its own scope " + strings.Repeat("x", 1000)},
		{Name: "elsewhere", Result: CacheHit},               // not a cache of this step
		{Name: "pip", Result: CacheMiss},                    // twice
		{Name: "models", Result: "triumph"},                 // not an outcome
		{Name: "models", Result: CacheHit, Restored: "wow"}, // twice, too
	}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	got := d.StepList[0].Caches
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if got[0].Name != "pip" || got[0].Result != CacheSaved || got[0].Restored != CacheFallback || got[0].Bytes != 1234 || got[0].Key != key || got[0].Scope != "default" || got[0].Seconds != 4.5 {
		t.Errorf("%+v", got[0])
	}
	if got[1].Name != "models" || got[1].Key != "" || got[1].Result != CacheNotSaved || strings.ContainsAny(got[1].Reason, "\n\x01") || len(got[1].Reason) > 410 {
		t.Errorf("%+v", got[1])
	}
	// The live update of the finished step carries them too.
	v, err := e.stepView(id, "test")
	if err != nil || len(v.Caches) != 2 {
		t.Fatalf("%+v %v", v, err)
	}
	// A step with no cache says nothing about any.
	id2 := create(t, e, linear, Trigger{Event: "manual"})
	j2 := take(t, e)
	if err := e.Finish(j2.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Caches: []CacheResult{{Name: "pip", Result: CacheHit}}}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.GetRun(id2); len(d.StepList[0].Caches) != 0 {
		t.Fatalf("%+v", d.StepList[0].Caches)
	}
	// A failed attempt that is retried starts clean.
	id3 := create(t, e, strings.Replace(cachedStep, "    run: pytest\n", "    run: pytest\n    retry: 1\n", 1), Trigger{Event: "manual"})
	j3 := take(t, e)
	one := 1
	if err := e.Finish(j3.LeaseID, Result{Outcome: "failed", ExitCode: &one, Error: "exit 1", Caches: []CacheResult{{Name: "pip", Result: CacheMiss}}}); err != nil {
		t.Fatal(err)
	}
	if d, _ := e.GetRun(id3); len(d.StepList[0].Caches) != 0 || d.StepList[0].Status != StepQueuedS {
		t.Fatalf("%+v", d.StepList[0])
	}
}

// A server that keeps no store tells the runner, which then uses no cache.
func TestCacheProblemIsToldToTheRunner(t *testing.T) {
	e, _ := newEngine(t)
	create(t, e, cachedStep, Trigger{Event: "manual"})
	j := take(t, e)
	if j.Cache == nil || !strings.Contains(j.Cache.Problem, "keeps no caches") {
		t.Fatalf("%+v", j.Cache)
	}
	// Lookups and saves say so too.
	res, err := e.LookupCache(j.LeaseID, CacheLookup{Name: "pip", Key: sum([]byte("k")), Platform: "linux/amd64"})
	if err != nil || res.Entry != nil || !strings.Contains(res.Reason, "no artifact store") {
		t.Fatalf("%+v %v", res, err)
	}
}
