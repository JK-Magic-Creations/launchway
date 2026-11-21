package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// pipelineWith is a one-step pipeline called name whose step declares one
// cache, shaped by mod.
func pipelineWith(t *testing.T, name string, mod func(*pipeline.Cache)) *pipeline.Pipeline {
	t.Helper()
	p := parse(t, strings.Replace(cacheSteps, "pipeline: demo", "pipeline: "+name, 1))
	for _, s := range p.Steps {
		c := &pipeline.Cache{Name: "deps", Paths: []string{".deps"}, Key: pipeline.CacheKey{Text: "v1"}}
		if mod != nil {
			mod(c)
		}
		s.Cache = append(s.Cache, c)
	}
	return p
}

// startPipeline makes a run of p and leases its step.
func (r *cacheRig) startPipeline(project string, p *pipeline.Pipeline, trig Trigger) *Job {
	r.t.Helper()
	run, err := r.e.CreateRun(NewRun{Project: project, Pipeline: p, Trigger: trig, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		r.t.Fatal(err)
	}
	j, err := r.e.Lease(context.Background(), runner, 0)
	if err != nil || j == nil || j.RunID != run.ID {
		r.t.Fatalf("leased %+v %v for run %s", j, err, run.ID)
	}
	if _, err := r.e.Start(j.LeaseID); err != nil {
		r.t.Fatal(err)
	}
	return j
}

func (r *cacheRig) finish(j *Job) {
	r.t.Helper()
	ok(r.t, r.e, j, nil)
}

// Two pipelines of a project that use one cache name do not see each other's
// entries, unless both name the same share.
func TestCachesAreSeparatePerPipelineUnlessShared(t *testing.T) {
	r := newCacheRig(t)
	key := cacheKey("lock")
	lint := r.startPipeline("shop", pipelineWith(t, "lint", nil), cacheOnMain)
	r.mustSave(lint, "deps", key, []byte("lint bytes"))
	r.finish(lint)

	release := r.startPipeline("shop", pipelineWith(t, "release", nil), cacheOnMain)
	if en := r.lookup(release, "deps", key, "latest"); en != nil {
		t.Fatalf("the release pipeline restored the lint pipeline's entry: %+v", en)
	}
	r.finish(release)
	again := r.startPipeline("shop", pipelineWith(t, "lint", nil), cacheOnMain)
	if en := r.lookup(again, "deps", key, ""); en == nil {
		t.Fatal("a pipeline did not find its own entry")
	}
	r.finish(again)

	share := func(c *pipeline.Cache) { c.Share = "toolchain" }
	a := r.startPipeline("shop", pipelineWith(t, "a", share), cacheOnMain)
	r.mustSave(a, "deps", key, []byte("shared bytes"))
	r.finish(a)
	b := r.startPipeline("shop", pipelineWith(t, "b", share), cacheOnMain)
	en := r.lookup(b, "deps", key, "")
	if en == nil {
		t.Fatal("a pipeline that names the same share did not find the entry")
	}
	if data, err := r.download(b, en.ID); err != nil || string(data) != "shared bytes" {
		t.Fatalf("%q %v", data, err)
	}
	r.finish(b)
	// A pipeline in no share, with the same cache name, still sees neither.
	c := r.startPipeline("shop", pipelineWith(t, "c", nil), cacheOnMain)
	if en := r.lookup(c, "deps", key, "latest"); en != nil && en.Scope == "default" {
		if data, _ := r.download(c, en.ID); string(data) == "shared bytes" {
			t.Fatalf("a pipeline outside the share restored its entry")
		}
	}
	r.finish(c)
}

// A sensitive cache is never read outside the scope that wrote it, and an
// entry saved as sensitive is not handed to a pull request even if the pull
// request's own pipeline says the cache is not sensitive.
func TestSensitiveCachesNeverLeaveTheirScope(t *testing.T) {
	r := newCacheRig(t)
	key := cacheKey("lock")
	sens := func(c *pipeline.Cache) { c.Sensitive = true }
	main := r.startPipeline("shop", pipelineWith(t, "ci", sens), cacheOnMain)
	r.mustSave(main, "deps", key, []byte("private"))
	r.finish(main)

	pr := r.startPipeline("shop", pipelineWith(t, "ci", sens), cacheOnPull(7))
	if en := r.lookup(pr, "deps", key, "latest"); en != nil {
		t.Fatalf("a pull request restored a sensitive entry of the default branch: %+v", en)
	}
	r.finish(pr)
	// The pull request edits its pipeline to say the cache is not sensitive.
	pr2 := r.startPipeline("shop", pipelineWith(t, "ci", nil), cacheOnPull(8))
	if en := r.lookup(pr2, "deps", key, "latest"); en != nil {
		t.Fatalf("a pull request that dropped sensitive: true restored the entry: %+v", en)
	}
	r.finish(pr2)
	// The default branch itself still restores it.
	again := r.startPipeline("shop", pipelineWith(t, "ci", sens), cacheOnMain)
	if en := r.lookup(again, "deps", key, ""); en == nil {
		t.Fatal("the writing scope lost its own sensitive entry")
	}
	r.finish(again)
}

// A pull request from a fork reads only what its own pull request wrote, not
// the default branch's caches.
func TestForkPullRequestsReadNothingFromTheProjectsCaches(t *testing.T) {
	r := newCacheRig(t)
	key := cacheKey("lock")
	main := r.startPipeline("shop", pipelineWith(t, "ci", nil), cacheOnMain)
	r.mustSave(main, "deps", key, []byte("built by the default branch"))
	r.finish(main)

	same := r.startPipeline("shop", pipelineWith(t, "ci", nil), cacheOnPull(3))
	if en := r.lookup(same, "deps", key, ""); en == nil {
		t.Fatal("a same-repository pull request lost the default branch's entry")
	}
	r.finish(same)
	fork := cacheOnPull(4)
	fork.Fork = true
	f := r.startPipeline("shop", pipelineWith(t, "ci", nil), fork)
	if en := r.lookup(f, "deps", key, "latest"); en != nil {
		t.Fatalf("a fork's pull request restored the default branch's entry: %+v", en)
	}
	r.mustSave(f, "deps", cacheKey("own"), []byte("fork's own"))
	r.finish(f)
	f2 := r.startPipeline("shop", pipelineWith(t, "ci", nil), fork)
	if en := r.lookup(f2, "deps", cacheKey("own"), ""); en == nil {
		t.Fatal("a fork's pull request lost its own entry")
	}
}
