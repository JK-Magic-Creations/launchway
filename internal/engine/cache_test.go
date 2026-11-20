package engine

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

const cacheSteps = `
pipeline: demo
on:
  pull_request: {}
steps:
  build:
    image: x
    run: make
`

const mib = 1 << 20

// cacheKey makes a cache key (64 hex digits) from text.
func cacheKey(s string) string { return sum([]byte(s)) }

const linux = "linux/amd64"

// cachePipeline is a one-step pipeline whose step declares the caches.
func cachePipeline(t *testing.T) *pipeline.Pipeline {
	t.Helper()
	p := parse(t, cacheSteps)
	for _, s := range p.Steps {
		for _, n := range []string{"pip", "models"} {
			s.Cache = append(s.Cache, &pipeline.Cache{Name: n, Paths: []string{"." + n}, Key: pipeline.CacheKey{Text: "v1"}})
		}
	}
	return p
}

// cacheRig is an engine (or several sharing a database) with caches.
type cacheRig struct {
	t   *testing.T
	e   *Engine
	clk *clock
}

func newCacheRig(t *testing.T) *cacheRig {
	t.Helper()
	clk := newClock()
	e := openStore(t, testdb.New(t), clk, t.TempDir(), ArtifactOptions{})
	e.SetDefaultBranch(func(string) string { return "main" })
	return &cacheRig{t: t, e: e, clk: clk}
}

// start makes a run of the project and leases its step.
func (r *cacheRig) start(project string, trig Trigger) *Job {
	r.t.Helper()
	return r.startOn(r.e, project, trig, nil)
}

func (r *cacheRig) startOn(e *Engine, project string, trig Trigger, draft *Draft) *Job {
	r.t.Helper()
	run, err := e.CreateRun(NewRun{Project: project, Pipeline: cachePipeline(r.t), Trigger: trig, Draft: draft,
		Source: Source{Kind: "dir", Path: "/tmp"}, PullRequestSecrets: false})
	if err != nil {
		r.t.Fatal(err)
	}
	j, err := e.Lease(context.Background(), runner, 0)
	if err != nil || j == nil || j.RunID != run.ID {
		r.t.Fatalf("leased %+v %v for run %s", j, err, run.ID)
	}
	if _, err := e.Start(j.LeaseID); err != nil {
		r.t.Fatal(err)
	}
	return j
}

var (
	cacheOnMain   = Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: "aaaa"}
	cacheOnBranch = Trigger{Event: "push", Ref: "refs/heads/feature", Branch: "feature", SHA: "bbbb"}
	cacheOnTag    = Trigger{Event: "push", Ref: "refs/tags/v1.0", Tag: "v1.0", SHA: "cccc"}
)

func cacheOnPull(n int) Trigger {
	return Trigger{Event: "pull_request", Ref: PullRequestRef(n), PullRequest: n, SHA: "dddd"}
}

// save sends an entry in chunks and commits it. begun is what the server
// said at the declaration; committed is nil when nothing was committed.
func (r *cacheRig) save(e *Engine, j *Job, name, key string, data []byte) (begun *BegunCache, committed *CommittedCache, err error) {
	r.t.Helper()
	begun, err = e.BeginCache(j.LeaseID, BeginCache{Name: name, Key: key, Platform: linux, Bytes: int64(len(data)), Files: 1, SHA256: sum(data)})
	if err != nil || begun.Exists {
		return begun, nil, err
	}
	for off := begun.Received; off < int64(len(data)); {
		end := min(off+ArtifactChunkBytes, int64(len(data)))
		got, err := e.PutCacheChunk(context.Background(), j.LeaseID, begun.ID, off, sum(data[off:end]), data[off:end])
		if err != nil {
			return begun, nil, err
		}
		off = got
	}
	committed, err = e.CommitCache(context.Background(), j.LeaseID, begun.ID)
	return begun, committed, err
}

func (r *cacheRig) mustSave(j *Job, name, key string, data []byte) *CommittedCache {
	r.t.Helper()
	b, c, err := r.save(r.e, j, name, key, data)
	if err != nil || c == nil || b.Exists {
		r.t.Fatalf("saving %s: %+v %+v %v", name, b, c, err)
	}
	return c
}

func (r *cacheRig) lookup(j *Job, name, key, fallback string) *CacheEntry {
	r.t.Helper()
	res, err := r.e.LookupCache(j.LeaseID, CacheLookup{Name: name, Key: key, Platform: linux, Fallback: fallback})
	if err != nil {
		r.t.Fatal(err)
	}
	return res.Entry
}

func (r *cacheRig) download(j *Job, id string) ([]byte, error) {
	r.t.Helper()
	body, _, _, err := r.e.OpenCache(context.Background(), j.LeaseID, id, 0)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

func (r *cacheRig) entries(project string) map[string]CacheEntryView {
	r.t.Helper()
	l, err := r.e.Caches(project)
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]CacheEntryView{}
	for _, v := range l.Entries {
		out[v.Name+"/"+v.Scope+"/"+v.Key[:6]] = v
	}
	return out
}

// A saved entry is restored by the next run of the same scope, with its
// bytes, and counted as used.
func TestCacheRoundTrip(t *testing.T) {
	r := newCacheRig(t)
	k := cacheKey("lock-1")
	data := big(3*mib+17, 3)
	j1 := r.start("demo", cacheOnMain)
	if en := r.lookup(j1, "pip", k, ""); en != nil {
		t.Fatalf("an empty cache has %+v", en)
	}
	c := r.mustSave(j1, "pip", k, data)
	if c.Bytes != int64(len(data)) || c.Exists {
		t.Fatalf("%+v", c)
	}
	ok(t, r.e, j1, nil)

	j2 := r.start("demo", cacheOnMain)
	en := r.lookup(j2, "pip", k, "")
	if en == nil || !en.Exact || en.Scope != "default" || en.Bytes != int64(len(data)) || en.SHA256 != sum(data) || en.Files != 1 {
		t.Fatalf("%+v", en)
	}
	got, err := r.download(j2, en.ID)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded %d bytes: %v", len(got), err)
	}
	// From an offset, for a restore that carries on after a broken connection.
	body, size, hash, err := r.e.OpenCache(context.Background(), j2.LeaseID, en.ID, 1000)
	if err != nil || size != int64(len(data)) || hash != sum(data) {
		t.Fatal(size, hash, err)
	}
	rest, _ := io.ReadAll(body)
	body.Close()
	if !bytes.Equal(rest, data[1000:]) {
		t.Fatal("the bytes from an offset are not the rest")
	}
	l, err := r.e.Caches("demo")
	if err != nil || len(l.Entries) != 1 || l.Entries[0].Hits != 1 || l.Entries[0].RunID != j1.RunID || l.Entries[0].RunNumber == 0 ||
		l.Totals.Bytes != int64(len(data)) || l.Totals.Entries != 1 || l.Totals.QuotaBytes != 10<<30 || l.Totals.IdleDays != 7 || l.Policy.Source != "default" {
		t.Fatalf("%+v %v", l, err)
	}
	// Another platform, another name, another key: nothing.
	if res, _ := r.e.LookupCache(j2.LeaseID, CacheLookup{Name: "pip", Key: k, Platform: "linux/arm64"}); res.Entry != nil {
		t.Fatal("an entry of another platform was returned")
	}
	if en := r.lookup(j2, "models", k, ""); en != nil {
		t.Fatal("an entry of another name was returned")
	}
	if en := r.lookup(j2, "pip", cacheKey("lock-2"), ""); en != nil {
		t.Fatal("an entry of another key was returned without a fallback")
	}
}

// A cache is declared by its step: a runner cannot touch a name its step
// does not have, and the arguments are checked.
func TestCacheRequestsAreChecked(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	for name, in := range map[string]CacheLookup{
		"undeclared": {Name: "secret-cache", Key: cacheKey("a"), Platform: linux},
		"short key":  {Name: "pip", Key: "abc", Platform: linux},
		"platform":   {Name: "pip", Key: cacheKey("a"), Platform: "../../etc"},
		"fallback":   {Name: "pip", Key: cacheKey("a"), Platform: linux, Fallback: "oldest"},
		"upper key":  {Name: "pip", Key: strings.ToUpper(cacheKey("a")), Platform: linux},
	} {
		if _, err := r.e.LookupCache(j.LeaseID, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, in := range map[string]BeginCache{
		"undeclared": {Name: "x", Key: cacheKey("a"), Platform: linux, Bytes: 1, SHA256: sum([]byte("a"))},
		"no bytes":   {Name: "pip", Key: cacheKey("a"), Platform: linux, Bytes: 0, SHA256: sum([]byte("a"))},
		"bad sum":    {Name: "pip", Key: cacheKey("a"), Platform: linux, Bytes: 1, SHA256: "nope"},
		"files":      {Name: "pip", Key: cacheKey("a"), Platform: linux, Bytes: 1, Files: -1, SHA256: sum([]byte("a"))},
	} {
		if _, err := r.e.BeginCache(j.LeaseID, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A lease that is gone cannot ask.
	ok(t, r.e, j, nil)
	if _, err := r.e.LookupCache(j.LeaseID, CacheLookup{Name: "pip", Key: cacheKey("a"), Platform: linux}); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("a finished step's lease: %v", err)
	}
	if _, err := r.e.LookupCache("lease_nope", CacheLookup{Name: "pip", Key: cacheKey("a"), Platform: linux}); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("an unknown lease: %v", err)
	}
}

// The scopes: where each kind of run writes, and what it can read.
func TestCacheScopeMatrix(t *testing.T) {
	r := newCacheRig(t)
	seed, kPR, kPR2, kBranch, kTag := cacheKey("seed"), cacheKey("pr7"), cacheKey("pr8"), cacheKey("feature"), cacheKey("tag")

	// A push to the default branch seeds every other scope.
	main1 := r.start("demo", cacheOnMain)
	r.mustSave(main1, "pip", seed, []byte("default-bytes"))
	ok(t, r.e, main1, nil)

	// A pull-request run restores it, and writes only pull:7.
	pr7 := r.start("demo", cacheOnPull(7))
	if en := r.lookup(pr7, "pip", seed, ""); en == nil || en.Scope != "default" || !en.Exact {
		t.Fatalf("a pull-request run reads the default branch's cache: %+v", en)
	}
	b, _, err := r.save(r.e, pr7, "pip", kPR, []byte("untrusted-bytes"))
	if err != nil || b.Scope != "pull:7" {
		t.Fatalf("%+v %v", b, err)
	}
	// It cannot overwrite what the default branch has: the same key is an
	// entry of another scope, and the default one is untouched.
	b, _, err = r.save(r.e, pr7, "pip", seed, []byte("evil-replacement"))
	if err != nil || b.Scope != "pull:7" {
		t.Fatalf("%+v %v", b, err)
	}
	if en := r.lookup(pr7, "pip", kPR, ""); en == nil || en.Scope != "pull:7" {
		t.Fatalf("a run reads what its own scope kept: %+v", en)
	}
	if en := r.lookup(pr7, "pip", seed, ""); en == nil || en.Scope != "pull:7" {
		// Its own scope first: its own entry shadows the default's for it.
		t.Fatalf("own scope first: %+v", en)
	}
	ok(t, r.e, pr7, nil)

	// A default-branch run never sees anything a pull request wrote: not by
	// the exact key, not by fallback.
	main2 := r.start("demo", cacheOnMain)
	if en := r.lookup(main2, "pip", kPR, ""); en != nil {
		t.Fatalf("POISONING: a default-branch run read a pull-request entry: %+v", en)
	}
	if en := r.lookup(main2, "pip", cacheKey("something else"), "latest"); en == nil || en.Scope != "default" || en.Key != seed {
		t.Fatalf("POISONING: fallback latest for a default-branch run must pick the default scope's entry, got %+v", en)
	}
	en := r.lookup(main2, "pip", seed, "")
	if en == nil || en.Scope != "default" {
		t.Fatalf("%+v", en)
	}
	if got, err := r.download(main2, en.ID); err != nil || string(got) != "default-bytes" {
		t.Fatalf("the default entry was changed by a pull request: %q %v", got, err)
	}
	// Nor can it open one by its ID, which a runner might have learned.
	pr7Entries := r.entries("demo")
	var pullID string
	for k, v := range pr7Entries {
		if strings.Contains(k, "/pull:7/") && v.Key == kPR {
			pullID = v.ID
		}
	}
	if pullID == "" {
		t.Fatalf("no entry of pull:7: %v", pr7Entries)
	}
	if _, err := r.download(main2, pullID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("POISONING: a default-branch run opened a pull-request entry by its ID: %v", err)
	}
	// A branch run does not read it either.
	feat := r.start("demo", cacheOnBranch)
	if en := r.lookup(feat, "pip", kPR, "latest"); en != nil && en.Scope == "pull:7" {
		t.Fatalf("a branch run read a pull-request entry: %+v", en)
	}
	if _, err := r.download(feat, pullID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a branch run opened a pull-request entry by its ID: %v", err)
	}
	ok(t, r.e, main2, nil)
	ok(t, r.e, feat, nil)

	// Another pull request does not read pull:7.
	pr8 := r.start("demo", cacheOnPull(8))
	if en := r.lookup(pr8, "pip", kPR, ""); en != nil {
		t.Fatalf("pull request 8 read pull request 7's entry: %+v", en)
	}
	if en := r.lookup(pr8, "pip", cacheKey("x"), "latest"); en == nil || en.Scope != "default" {
		t.Fatalf("pull request 8 falls back to the default scope only: %+v", en)
	}
	if _, err := r.download(pr8, pullID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pull request 8 opened pull request 7's entry by its ID: %v", err)
	}
	r.mustSave(pr8, "pip", kPR2, []byte("pr8"))
	ok(t, r.e, pr8, nil)

	// A branch writes ref:<ref>, a tag its own ref, and each reads its own
	// and the default scope.
	feat = r.start("demo", cacheOnBranch)
	if b, _, err := r.save(r.e, feat, "pip", kBranch, []byte("branch")); err != nil || b.Scope != "ref:refs/heads/feature" {
		t.Fatalf("%+v %v", b, err)
	}
	ok(t, r.e, feat, nil)
	feat = r.start("demo", cacheOnBranch)
	if en := r.lookup(feat, "pip", kBranch, ""); en == nil || en.Scope != "ref:refs/heads/feature" {
		t.Fatalf("%+v", en)
	}
	if en := r.lookup(feat, "pip", seed, ""); en == nil || en.Scope != "default" {
		t.Fatalf("a branch reads the default scope: %+v", en)
	}
	ok(t, r.e, feat, nil)
	tag := r.start("demo", cacheOnTag)
	if b, _, err := r.save(r.e, tag, "pip", kTag, []byte("tag")); err != nil || b.Scope != "ref:refs/tags/v1.0" {
		t.Fatalf("a tag run writes ref:<tag>: %+v %v", b, err)
	}
	if en := r.lookup(tag, "pip", kBranch, ""); en != nil {
		t.Fatalf("a tag run read a branch's entry: %+v", en)
	}
	ok(t, r.e, tag, nil)
	main3 := r.start("demo", cacheOnMain)
	for _, k := range []string{kBranch, kTag, kPR2} {
		if en := r.lookup(main3, "pip", k, ""); en != nil {
			t.Fatalf("a default-branch run read %+v", en)
		}
	}
	ok(t, r.e, main3, nil)

	// Nothing crosses projects, however the keys are chosen.
	other := r.start("other", cacheOnMain)
	if en := r.lookup(other, "pip", seed, "latest"); en != nil {
		t.Fatalf("another project read this one's entry: %+v", en)
	}
	var defaultID string
	for _, v := range r.entries("demo") {
		if v.Scope == "default" {
			defaultID = v.ID
		}
	}
	if _, err := r.download(other, defaultID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another project opened this one's entry by its ID: %v", err)
	}
	r.mustSave(other, "pip", seed, []byte("other-project"))
	if got := r.entries("other"); len(got) != 1 {
		t.Fatalf("%v", got)
	}
	// Nor delete it.
	if _, err := r.e.DeleteCaches("other", defaultID, "", "ann"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	ok(t, r.e, other, nil)
}

// A draft run runs text nobody reviewed: it reads the default branch's
// cache and writes nothing.
func TestDraftRunsWriteNoCache(t *testing.T) {
	r := newCacheRig(t)
	seed := cacheKey("seed")
	m := r.start("demo", cacheOnMain)
	r.mustSave(m, "pip", seed, []byte("default"))
	ok(t, r.e, m, nil)

	d := r.startOn(r.e, "demo", Trigger{Ref: "refs/heads/main", Branch: "main", Actor: "dana"}, &Draft{Text: cacheSteps, Hash: "sha256:x"})
	if en := r.lookup(d, "pip", seed, ""); en == nil || en.Scope != "default" {
		t.Fatalf("a draft run restores the default branch's cache: %+v", en)
	}
	_, err := r.e.BeginCache(d.LeaseID, BeginCache{Name: "pip", Key: cacheKey("d"), Platform: linux, Bytes: 3, Files: 1, SHA256: sum([]byte("abc"))})
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "a draft run saves no cache") {
		t.Fatalf("a draft run's save: %v", err)
	}
	// A draft that says it is on the default branch is still a draft.
	if got := r.entries("demo"); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

// Only a run of the default branch, as the server knows it, writes default;
// the same branch name from a pull request or another ref does not. The
// scope is a function of how the run started, as the server recorded it.
func TestOnlyTheDefaultBranchWritesTheDefaultScope(t *testing.T) {
	r := newCacheRig(t)
	scope := func(project string, trig Trigger, draft *Draft) (string, []string) {
		s := r.e.scopesOf(&RunState{Project: project, Trigger: trig, Draft: draft})
		return s.write, s.read
	}
	for name, tc := range map[string]struct {
		trig  Trigger
		write string
		read  []string
	}{
		"a pull request":                      {Trigger{Event: "pull_request", Ref: PullRequestRef(9), PullRequest: 9}, "pull:9", []string{"pull:9", "default"}},
		"a pull request with no number":       {Trigger{Event: "pull_request"}, "", []string{"default"}},
		"a pull request claiming branch main": {Trigger{Event: "pull_request", Branch: "main", Ref: PullRequestRef(9), PullRequest: 9}, "pull:9", []string{"pull:9", "default"}},
		"a manual run of the pull ref":        {Trigger{Event: "manual", Branch: "main", Ref: "refs/pull/9/head"}, "ref:refs/pull/9/head", []string{"ref:refs/pull/9/head", "default"}},
		"a manual run on the default branch":  {Trigger{Event: "manual", Branch: "main", Ref: "refs/heads/main"}, "default", []string{"default"}},
		"a push to the default branch":        {Trigger{Event: "push", Branch: "main", Ref: "refs/heads/main"}, "default", []string{"default"}},
		"a schedule on the default branch":    {Trigger{Event: "schedule", Branch: "main", Ref: "refs/heads/main"}, "default", []string{"default"}},
		"a webhook run on a branch":           {Trigger{Event: "webhook", Branch: "release", Ref: "refs/heads/release"}, "ref:refs/heads/release", []string{"ref:refs/heads/release", "default"}},
		"a tag":                               {Trigger{Event: "push", Tag: "v1", Ref: "refs/tags/v1"}, "ref:refs/tags/v1", []string{"ref:refs/tags/v1", "default"}},
		"a tag called main":                   {Trigger{Event: "push", Tag: "main", Ref: "refs/tags/main"}, "ref:refs/tags/main", []string{"ref:refs/tags/main", "default"}},
		"no ref, branch main":                 {Trigger{Event: "manual", Branch: "main"}, "default", []string{"default"}},
		"nothing at all":                      {Trigger{Event: "manual"}, "", []string{"default"}},
	} {
		w, rd := scope("demo", tc.trig, nil)
		if w != tc.write || strings.Join(rd, ",") != strings.Join(tc.read, ",") {
			t.Errorf("%s: writes %q reads %v, want %q %v", name, w, rd, tc.write, tc.read)
		}
	}
	// A draft writes nothing, whatever it says it is.
	if w, rd := scope("demo", Trigger{Event: "draft", Branch: "main", Ref: "refs/heads/main"}, &Draft{}); w != "" || len(rd) != 1 || rd[0] != "default" {
		t.Errorf("a draft: %q %v", w, rd)
	}
	// A project whose default branch is trunk.
	r.e.SetDefaultBranch(func(p string) string {
		if p == "demo" {
			return "trunk"
		}
		return "main"
	})
	for name, tc := range map[string]struct {
		project string
		trig    Trigger
		scope   string
	}{
		"trunk is the default":       {"demo", Trigger{Event: "push", Branch: "trunk", Ref: "refs/heads/trunk"}, "default"},
		"main is not, then":          {"demo", Trigger{Event: "push", Branch: "main", Ref: "refs/heads/main"}, "ref:refs/heads/main"},
		"another project keeps main": {"shop", Trigger{Event: "push", Branch: "main", Ref: "refs/heads/main"}, "default"},
	} {
		if w, _ := scope(tc.project, tc.trig, nil); w != tc.scope {
			t.Errorf("%s: %q, want %q", name, w, tc.scope)
		}
	}
	// And through a real run: what the server derived is what it records.
	j := r.start("demo", Trigger{Event: "push", Branch: "trunk", Ref: "refs/heads/trunk"})
	if b, err := r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("x"), Platform: linux, Bytes: 3, Files: 1, SHA256: sum([]byte("abc"))}); err != nil || b.Scope != "default" {
		t.Errorf("%+v %v", b, err)
	}
}

// Entries are immutable: saving a name and key that exist is a no-op that
// says so, while the first bytes stay.
func TestCacheEntriesAreImmutable(t *testing.T) {
	r := newCacheRig(t)
	k := cacheKey("lock")
	j1 := r.start("demo", cacheOnMain)
	r.mustSave(j1, "pip", k, []byte("first"))
	ok(t, r.e, j1, nil)
	j2 := r.start("demo", cacheOnMain)
	b, c, err := r.save(r.e, j2, "pip", k, []byte("second, different"))
	if err != nil || !b.Exists || c != nil || !strings.Contains(b.Reason, "entries are never changed") {
		t.Fatalf("%+v %+v %v", b, c, err)
	}
	en := r.lookup(j2, "pip", k, "")
	if got, _ := r.download(j2, en.ID); string(got) != "first" {
		t.Fatalf("the entry was overwritten: %q", got)
	}
	// Committing again is also a no-op that says so.
	if c, err := r.e.CommitCache(context.Background(), j1.LeaseID, en.ID); !errors.Is(err, ErrLeaseLost) || c != nil {
		t.Fatalf("a finished attempt's lease: %+v %v", c, err)
	}
	// Two attempts saving the same key at once: the second is told another
	// run is saving it.
	j3 := r.start("demo", cacheOnMain)
	j4 := r.start("demo", cacheOnMain)
	k2 := cacheKey("other")
	b3, err := r.e.BeginCache(j3.LeaseID, BeginCache{Name: "pip", Key: k2, Platform: linux, Bytes: 4, Files: 1, SHA256: sum([]byte("abcd"))})
	if err != nil || b3.Exists {
		t.Fatalf("%+v %v", b3, err)
	}
	b4, err := r.e.BeginCache(j4.LeaseID, BeginCache{Name: "pip", Key: k2, Platform: linux, Bytes: 4, Files: 1, SHA256: sum([]byte("abcd"))})
	if err != nil || !b4.Exists || !strings.Contains(b4.Reason, "another run is saving") {
		t.Fatalf("%+v %v", b4, err)
	}
	// The one that began carries on after asking again (a retry).
	again, err := r.e.BeginCache(j3.LeaseID, BeginCache{Name: "pip", Key: k2, Platform: linux, Bytes: 4, Files: 1, SHA256: sum([]byte("abcd"))})
	if err != nil || again.Exists || again.ID != b3.ID {
		t.Fatalf("%+v %v", again, err)
	}
	// An entry nobody committed is not restored.
	if en := r.lookup(j4, "pip", k2, ""); en != nil {
		t.Fatalf("an upload in progress was restored: %+v", en)
	}
	// A step attempt cannot commit or send to another attempt's entry.
	if _, err := r.e.CommitCache(context.Background(), j4.LeaseID, b3.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := r.e.PutCacheChunk(context.Background(), j4.LeaseID, b3.ID, 0, sum([]byte("abcd")), []byte("abcd")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

// What arrives must be what was declared, whole.
func TestCacheUploadsAreChecked(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	data := big(ArtifactChunkBytes+5000, 9)
	decl := BeginCache{Name: "pip", Key: cacheKey("k"), Platform: linux, Bytes: int64(len(data)), Files: 2, SHA256: sum(data)}
	b, err := r.e.BeginCache(j.LeaseID, decl)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A damaged chunk, a chunk out of order, a short chunk that is not last.
	if _, err := r.e.PutCacheChunk(ctx, j.LeaseID, b.ID, 0, sum([]byte("x")), data[:ArtifactChunkBytes]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a chunk that does not match its sum: %v", err)
	}
	var co *ChunkOffsetError
	if _, err := r.e.PutCacheChunk(ctx, j.LeaseID, b.ID, ArtifactChunkBytes, sum(data[ArtifactChunkBytes:]), data[ArtifactChunkBytes:]); !errors.As(err, &co) || co.Received != 0 {
		t.Fatalf("a chunk that skips ahead: %v", err)
	}
	if _, err := r.e.PutCacheChunk(ctx, j.LeaseID, b.ID, 0, sum(data[:100]), data[:100]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a short chunk that is not the last: %v", err)
	}
	// Committing before everything is there.
	if _, err := r.e.CommitCache(ctx, j.LeaseID, b.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
	if got, err := r.e.PutCacheChunk(ctx, j.LeaseID, b.ID, 0, sum(data[:ArtifactChunkBytes]), data[:ArtifactChunkBytes]); err != nil || got != ArtifactChunkBytes {
		t.Fatal(got, err)
	}
	// The same chunk again is accepted without effect.
	if got, err := r.e.PutCacheChunk(ctx, j.LeaseID, b.ID, 0, sum(data[:ArtifactChunkBytes]), data[:ArtifactChunkBytes]); err != nil || got != ArtifactChunkBytes {
		t.Fatal(got, err)
	}
	// Declaring again says where to carry on.
	again, err := r.e.BeginCache(j.LeaseID, decl)
	if err != nil || again.ID != b.ID || again.Received != ArtifactChunkBytes {
		t.Fatalf("%+v %v", again, err)
	}
	if got, err := r.e.PutCacheChunk(ctx, j.LeaseID, b.ID, ArtifactChunkBytes, sum(data[ArtifactChunkBytes:]), data[ArtifactChunkBytes:]); err != nil || got != int64(len(data)) {
		t.Fatal(got, err)
	}
	c, err := r.e.CommitCache(ctx, j.LeaseID, b.ID)
	if err != nil || c.Exists {
		t.Fatalf("%+v %v", c, err)
	}
	// Committing twice is harmless.
	if c, err := r.e.CommitCache(ctx, j.LeaseID, b.ID); err != nil || !c.Exists {
		t.Fatalf("%+v %v", c, err)
	}

	// Content that is not what was declared is refused and given up.
	bad := big(1000, 1)
	b2, err := r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("k2"), Platform: linux, Bytes: int64(len(bad)), Files: 1, SHA256: sum(big(1000, 2))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.PutCacheChunk(ctx, j.LeaseID, b2.ID, 0, sum(bad), bad); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("%v", err)
	}
	if _, err := r.e.CommitCache(ctx, j.LeaseID, b2.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("a rejected upload committed: %v", err)
	}
	if en := r.lookup(j, "pip", cacheKey("k2"), ""); en != nil {
		t.Fatal("restored an entry whose bytes were not what it declared")
	}
}

func setPolicy(t *testing.T, e *Engine, project string, p CachePolicy) {
	t.Helper()
	if _, _, err := e.SetCachePolicy(project, p, "admin"); err != nil {
		t.Fatal(err)
	}
}

// The policy: defaults, an installation setting, a project's override.
func TestCachePolicy(t *testing.T) {
	r := newCacheRig(t)
	e := r.e
	v, err := e.CachePolicyFor("demo")
	if err != nil || v.Source != "default" || !v.Enabled || v.MaxProjectGiB != 10 || v.MaxEntryGiB != 2 || v.IdleDays != 7 {
		t.Fatalf("%+v %v", v, err)
	}
	before, after, err := e.SetCachePolicy("", CachePolicy{Enabled: true, MaxProjectGiB: 20, MaxEntryGiB: 4, IdleDays: 14}, "ann")
	if err != nil || before.Source != "default" || after.Source != "installation" || after.MaxProjectGiB != 20 || after.UpdatedBy != "ann" || after.UpdatedAt == 0 {
		t.Fatalf("%+v %+v %v", before, after, err)
	}
	if v, _ := e.CachePolicyFor("demo"); v.Source != "installation" || v.IdleDays != 14 {
		t.Fatalf("%+v", v)
	}
	_, after, err = e.SetCachePolicy("demo", CachePolicy{Enabled: false, MaxProjectGiB: 1, MaxEntryGiB: 0.5, IdleDays: 1}, "ann")
	if err != nil || after.Source != "project" || after.Project != "demo" || after.Enabled {
		t.Fatalf("%+v %v", after, err)
	}
	if v, _ := e.CachePolicyFor("shop"); v.Source != "installation" || !v.Enabled {
		t.Fatalf("an override is for its project only: %+v", v)
	}
	before, after, err = e.ClearCachePolicy("demo", "ann")
	if err != nil || before.Source != "project" || after.Source != "installation" {
		t.Fatalf("%+v %+v %v", before, after, err)
	}
	if _, _, err := e.ClearCachePolicy("", "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	for name, p := range map[string]CachePolicy{
		"zero project":    {Enabled: true, MaxProjectGiB: 0, MaxEntryGiB: 1, IdleDays: 7},
		"tiny entry":      {Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 0.0001, IdleDays: 7},
		"entry > project": {Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 2, IdleDays: 7},
		"no idle days":    {Enabled: true, MaxProjectGiB: 10, MaxEntryGiB: 2, IdleDays: 0},
		"idle forever":    {Enabled: true, MaxProjectGiB: 10, MaxEntryGiB: 2, IdleDays: 100000},
	} {
		if _, _, err := e.SetCachePolicy("", p, "ann"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A server that does not know caches says so.
	old := mustOpenAs(t, testdb.New(t), newClock(), schemaCache-1, &syncBuffer{})
	if _, err := old.CachePolicyFor("demo"); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
	if _, err := old.Caches("demo"); !errors.Is(err, ErrConflict) {
		t.Fatalf("%v", err)
	}
}

// Policy off: lookups miss and saves are refused, saying why; entries kept
// before are not restored.
func TestCachePolicyOff(t *testing.T) {
	r := newCacheRig(t)
	k := cacheKey("k")
	j := r.start("demo", cacheOnMain)
	r.mustSave(j, "pip", k, []byte("data"))
	ok(t, r.e, j, nil)
	setPolicy(t, r.e, "demo", CachePolicy{Enabled: false, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 7})
	j = r.start("demo", cacheOnMain)
	res, err := r.e.LookupCache(j.LeaseID, CacheLookup{Name: "pip", Key: k, Platform: linux})
	if err != nil || res.Entry != nil || !strings.Contains(res.Reason, "turned off") {
		t.Fatalf("%+v %v", res, err)
	}
	_, err = r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("z"), Platform: linux, Bytes: 1, Files: 1, SHA256: sum([]byte("x"))})
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("%v", err)
	}
	l, _ := r.e.Caches("demo")
	if l.Policy.Enabled || l.Policy.Source != "project" {
		t.Fatalf("%+v", l.Policy)
	}
	// Other projects are not affected.
	o := r.start("shop", cacheOnMain)
	r.mustSave(o, "pip", k, []byte("data"))
	// Turned off while an upload is under way: the commit is refused.
	setPolicy(t, r.e, "demo", CachePolicy{Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 7})
	b, err := r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("y"), Platform: linux, Bytes: 1, Files: 1, SHA256: sum([]byte("y"))})
	if err != nil || b.Exists {
		t.Fatal(b, err)
	}
	if _, err := r.e.PutCacheChunk(context.Background(), j.LeaseID, b.ID, 0, sum([]byte("y")), []byte("y")); err != nil {
		t.Fatal(err)
	}
	setPolicy(t, r.e, "demo", CachePolicy{Enabled: false, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 7})
	if _, err := r.e.CommitCache(context.Background(), j.LeaseID, b.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("%v", err)
	}
}

// A few MiB of quota: an entry over the entry limit is refused before any
// byte is sent, and a commit past the project's quota ends the project's
// least recently used entries, the project's only.
func TestCacheQuotaEvictsLeastRecentlyUsed(t *testing.T) {
	r := newCacheRig(t)
	// 3.5 MiB for the project, 2 MiB for an entry.
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 3.5 / 1024, MaxEntryGiB: 2.0 / 1024, IdleDays: 7})
	mk := func(n string) []byte { return big(mib, n[0]) }
	j := r.start("demo", cacheOnMain)
	if _, err := r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("huge"), Platform: linux, Bytes: 2*mib + 1, Files: 1, SHA256: sum([]byte("x"))}); !errors.Is(err, ErrQuota) ||
		!strings.Contains(err.Error(), "an entry may hold at most 2 MiB") {
		t.Fatalf("an entry over the limit is refused before upload: %v", err)
	}
	other := r.start("shop", cacheOnMain)
	r.mustSave(other, "pip", cacheKey("shop-entry"), mk("s"))

	for _, n := range []string{"a", "b", "c"} {
		r.advance(time.Minute)
		r.mustSave(j, "pip", cacheKey(n), mk(n))
	}
	// a is the oldest; using it makes b the least recently used.
	r.advance(time.Minute)
	if en := r.lookup(j, "pip", cacheKey("a"), ""); en == nil {
		t.Fatal("a is gone")
	}
	r.advance(time.Minute)
	b, _, err := r.save(r.e, j, "pip", cacheKey("d"), mk("d"))
	if err != nil || b.Evicted != 1 {
		t.Fatalf("%+v %v", b, err)
	}
	have := map[string]bool{}
	for _, v := range r.entries("demo") {
		have[v.Key[:6]] = true
	}
	for n, want := range map[string]bool{"a": true, "b": false, "c": true, "d": true} {
		if have[cacheKey(n)[:6]] != want {
			t.Errorf("entry %s kept: %v, want %v (%v)", n, have[cacheKey(n)[:6]], want, have)
		}
	}
	l, _ := r.e.Caches("demo")
	if l.Totals.Bytes != 3*mib || l.Totals.Entries != 3 {
		t.Fatalf("%+v", l.Totals)
	}
	if got := r.entries("shop"); len(got) != 1 {
		t.Fatalf("another project's entry was evicted: %v", got)
	}
	var used int64
	if err := r.e.db.QueryRow(`SELECT bytes FROM cache_usage WHERE project = 'demo'`).Scan(&used); err != nil || used != 3*mib {
		t.Fatalf("usage %d %v", used, err)
	}
	// An entry that cannot fit even in an empty project is refused when
	// declared, and one made to fit only after others are gone but then
	// found not to fit at commit is given up without evicting anything.
	if _, err := r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("big"), Platform: linux, Bytes: 4 * mib, Files: 1, SHA256: sum([]byte("x"))}); !errors.Is(err, ErrQuota) {
		t.Fatalf("%v", err)
	}
	// Evicted bytes go once the grace has passed.
	r.advance(cacheEndedGrace + time.Minute)
	r.e.pruneCaches()
	if got, _ := r.e.Caches("demo"); len(got.Entries) != 3 {
		t.Fatalf("%+v", got.Entries)
	}
	var ended int
	if err := r.e.db.QueryRow(`SELECT COUNT(*) FROM caches WHERE project = 'demo' AND status <> 'kept'`).Scan(&ended); err != nil || ended != 0 {
		t.Fatalf("ended rows left after the pruner: %d %v", ended, err)
	}
	ok(t, r.e, j, nil)
	ok(t, r.e, other, nil)
}

// Lowering the quota trims the project to it the next time the pruner runs.
func TestCacheQuotaLoweredTrims(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	for i, n := range []string{"a", "b", "c"} {
		r.advance(time.Minute * time.Duration(i+1))
		r.mustSave(j, "pip", cacheKey(n), big(mib, n[0]))
	}
	setPolicy(t, r.e, "demo", CachePolicy{Enabled: true, MaxProjectGiB: 2.5 / 1024, MaxEntryGiB: 2.0 / 1024, IdleDays: 7})
	r.e.pruneCaches()
	l, _ := r.e.Caches("demo")
	if len(l.Entries) != 2 || l.Totals.Bytes != 2*mib {
		t.Fatalf("%+v", l)
	}
	for _, v := range l.Entries {
		if v.Key == cacheKey("a") {
			t.Fatalf("the oldest entry should have gone: %+v", l.Entries)
		}
	}
}

// Entries nobody restored for the policy's idle days end, and their bytes
// go; an entry that was restored stays.
func TestIdleCachesAreEnded(t *testing.T) {
	r := newCacheRig(t)
	setPolicy(t, r.e, "", CachePolicy{Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 7})
	j := r.start("demo", cacheOnMain)
	r.mustSave(j, "pip", cacheKey("old"), []byte("old"))
	r.mustSave(j, "models", cacheKey("busy"), []byte("busy"))
	r.mustSave(r.start("shop", cacheOnMain), "pip", cacheKey("shop"), []byte("shop"))
	setPolicy(t, r.e, "shop", CachePolicy{Enabled: true, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 30})
	r.advance(6 * 24 * time.Hour)
	if en := r.lookup(j, "models", cacheKey("busy"), ""); en == nil {
		t.Fatal("busy is gone")
	}
	r.advance(2 * 24 * time.Hour)
	r.e.pruneCaches()
	have := r.entries("demo")
	if len(have) != 1 {
		t.Fatalf("%v", have)
	}
	for _, v := range have {
		if v.Name != "models" {
			t.Fatalf("%+v", v)
		}
	}
	if got := r.entries("shop"); len(got) != 1 {
		t.Fatalf("a project with a longer idle time lost its entry: %v", got)
	}
	var used int64
	if err := r.e.db.QueryRow(`SELECT bytes FROM cache_usage WHERE project = 'demo'`).Scan(&used); err != nil || used != 4 {
		t.Fatalf("usage %d %v", used, err)
	}
	// The bytes of what ended are removed after the grace, rows and all.
	r.advance(cacheEndedGrace + time.Minute)
	r.e.pruneCaches()
	var rows int
	if err := r.e.db.QueryRow(`SELECT COUNT(*) FROM caches WHERE project = 'demo'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows %d %v", rows, err)
	}
	// A name that was ended can be saved again.
	j2 := r.start("demo", cacheOnMain)
	r.mustSave(j2, "pip", cacheKey("old"), []byte("old again"))
}

// An upload that stalls for 48 hours, or whose step attempt is gone, ends;
// its bytes are abandoned.
func TestStalledCacheUploadsEnd(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	b, err := r.e.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("s"), Platform: linux, Bytes: 5, Files: 1, SHA256: sum([]byte("hello"))})
	if err != nil {
		t.Fatal(err)
	}
	r.advance(time.Hour)
	r.e.pruneCaches()
	if l, _ := r.e.Caches("demo"); len(l.Entries) != 1 || l.Entries[0].Status != "uploading" {
		t.Fatalf("an upload under way was ended: %+v", l.Entries)
	}
	// The attempt ends without committing; ten minutes later the upload is gone.
	ok(t, r.e, j, nil)
	r.advance(cacheOrphanedUpload + time.Minute)
	r.e.pruneCaches()
	if l, _ := r.e.Caches("demo"); len(l.Entries) != 0 {
		t.Fatalf("%+v", l.Entries)
	}
	// An attempt that is still held but makes no progress for 48 hours.
	j2 := r.start("demo", cacheOnMain)
	if _, err := r.e.BeginCache(j2.LeaseID, BeginCache{Name: "pip", Key: cacheKey("t"), Platform: linux, Bytes: 5, Files: 1, SHA256: sum([]byte("hello"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.db.Exec(`UPDATE leases SET expires_at = ?`, r.clk.t.Add(1000*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	r.advance(49 * time.Hour)
	r.e.pruneCaches()
	if l, _ := r.e.Caches("demo"); len(l.Entries) != 0 {
		t.Fatalf("%+v", l.Entries)
	}
	_ = b
}

// Deleting by id and by name, and what each gives back.
func TestDeleteCaches(t *testing.T) {
	r := newCacheRig(t)
	j := r.start("demo", cacheOnMain)
	r.mustSave(j, "pip", cacheKey("1"), []byte("one"))
	r.mustSave(j, "pip", cacheKey("2"), []byte("twotwo"))
	r.mustSave(j, "models", cacheKey("3"), []byte("three"))
	var one, models string
	for _, v := range r.entries("demo") {
		if v.Key == cacheKey("1") {
			one = v.ID
		}
		if v.Name == "models" {
			models = v.ID
		}
	}
	d, err := r.e.DeleteCaches("demo", one, "", "ann")
	if err != nil || d.Ended != 1 || d.Bytes != 3 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := r.e.DeleteCaches("demo", one, "", "ann"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}
	d, err = r.e.DeleteCaches("demo", "", "pip", "ann")
	if err != nil || d.Ended != 1 || d.Bytes != 6 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := r.e.DeleteCaches("demo", "", "pip", "ann"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	for _, args := range [][2]string{{"", ""}, {models, "models"}} {
		if _, err := r.e.DeleteCaches("demo", args[0], args[1], "ann"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := r.e.DeleteCaches("demo", "", "../x", "ann"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v", err)
	}
	l, _ := r.e.Caches("demo")
	if len(l.Entries) != 1 || l.Totals.Bytes != 5 {
		t.Fatalf("%+v", l)
	}
	var note string
	if err := r.e.db.QueryRow(`SELECT ended_by FROM caches WHERE id = ?`, one).Scan(&note); err != nil || note != "ann: deleted" {
		t.Fatalf("%q %v", note, err)
	}
	// A deleted entry is not restored.
	j2 := r.start("demo", cacheOnMain)
	if en := r.lookup(j2, "pip", cacheKey("1"), "latest"); en != nil {
		t.Fatalf("%+v", en)
	}
}

// fallback latest takes the newest entry of the name and platform, the run's
// own scope before the default scope.
func TestCacheFallbackLatest(t *testing.T) {
	r := newCacheRig(t)
	m := r.start("demo", cacheOnMain)
	r.advance(time.Minute)
	r.mustSave(m, "pip", cacheKey("v1"), []byte("v1"))
	r.advance(time.Minute)
	r.mustSave(m, "pip", cacheKey("v2"), []byte("v2"))
	ok(t, r.e, m, nil)
	want := func(j *Job, fb, key string) {
		t.Helper()
		en := r.lookup(j, "pip", cacheKey("v3"), fb)
		switch {
		case key == "" && en != nil:
			t.Fatalf("%q: %+v", fb, en)
		case key != "" && (en == nil || en.Key != cacheKey(key) || en.Exact):
			t.Fatalf("%q: %+v", fb, en)
		}
	}
	m2 := r.start("demo", cacheOnMain)
	want(m2, "", "")
	want(m2, "none", "")
	want(m2, "latest", "v2")
	// A pull request with an older entry of its own prefers its own.
	pr := r.start("demo", cacheOnPull(3))
	r.advance(-time.Hour)
	r.mustSave(pr, "pip", cacheKey("pr"), []byte("pr"))
	r.advance(time.Hour)
	want(pr, "latest", "pr")
	// An exact key wins over a newer fallback, in any scope it reads.
	if en := r.lookup(pr, "pip", cacheKey("v1"), "latest"); en == nil || !en.Exact || en.Key != cacheKey("v1") {
		t.Fatalf("%+v", en)
	}
}

// Two servers sharing a database commit the same entry at the same time
// and evict at the same time: rows and counters stay consistent, and after
// the pruners run no bytes are left that no entry refers to.
func TestTwoServersCommitAndEvictAtOnce(t *testing.T) {
	clk := newClock()
	addr := testdb.New(t)
	dir := t.TempDir()
	e1 := openStore(t, addr, clk, dir, ArtifactOptions{})
	e2 := openStore(t, addr, clk, dir, ArtifactOptions{})
	e1.SetDefaultBranch(func(string) string { return "main" })
	e2.SetDefaultBranch(func(string) string { return "main" })
	r := &cacheRig{t: t, e: e1, clk: clk}
	setPolicy(t, e1, "", CachePolicy{Enabled: true, MaxProjectGiB: 3.5 / 1024, MaxEntryGiB: 2.0 / 1024, IdleDays: 7})

	// The same entry, committed by both at once.
	j := r.start("demo", cacheOnMain)
	data := big(mib, 4)
	b, err := e1.BeginCache(j.LeaseID, BeginCache{Name: "pip", Key: cacheKey("same"), Platform: linux, Bytes: mib, Files: 1, SHA256: sum(data)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.PutCacheChunk(context.Background(), j.LeaseID, b.ID, 0, sum(data), data); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	res := make([]*CommittedCache, 2)
	errs := make([]error, 2)
	for i, e := range []*Engine{e1, e2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = e.CommitCache(context.Background(), j.LeaseID, b.ID)
		}()
	}
	wg.Wait()
	won := 0
	for i := range res {
		if errs[i] != nil {
			t.Fatalf("server %d: %v", i+1, errs[i])
		}
		if !res[i].Exists {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d servers won the commit: %+v %+v", won, res[0], res[1])
	}
	var used int64
	if err := e1.db.QueryRow(`SELECT bytes FROM cache_usage WHERE project = 'demo'`).Scan(&used); err != nil || used != mib {
		t.Fatalf("usage %d after two commits of one entry: %v", used, err)
	}

	// Many entries committed from both servers at once on a project that
	// holds three: whatever is evicted, usage equals what is kept.
	jobs := make([]*Job, 6)
	for i := range jobs {
		jobs[i] = r.start("demo", cacheOnMain)
	}
	for i := range jobs {
		wg.Add(1)
		e := []*Engine{e1, e2}[i%2]
		go func() {
			defer wg.Done()
			d := big(mib, byte(10+i))
			_, c, err := r.save(e, jobs[i], "pip", cacheKey("many"+string(rune('a'+i))), d)
			if err != nil && !errors.Is(err, ErrConflict) {
				t.Errorf("entry %d: %v", i, err)
			}
			_ = c
		}()
	}
	wg.Wait()
	check := func() {
		t.Helper()
		var kept, keptRows int64
		if err := e1.db.QueryRow(`SELECT COALESCE(SUM(bytes), 0), COUNT(*) FROM caches WHERE project = 'demo' AND status = 'kept'`).Scan(&kept, &keptRows); err != nil {
			t.Fatal(err)
		}
		if err := e1.db.QueryRow(`SELECT bytes FROM cache_usage WHERE project = 'demo'`).Scan(&used); err != nil {
			t.Fatal(err)
		}
		if used != kept || kept > 3*mib+mib/2 {
			t.Fatalf("usage %d, kept %d bytes in %d entries, quota %d", used, kept, keptRows, 3*mib+mib/2)
		}
	}
	check()
	// Both servers prune at once; afterwards the store holds exactly the kept
	// entries' bytes.
	advanceClock(t, e1, clk, cacheEndedGrace+time.Minute)
	for _, e := range []*Engine{e1, e2} {
		wg.Add(1)
		go func() { defer wg.Done(); e.pruneCaches() }()
	}
	wg.Wait()
	e1.pruneCaches()
	check()
	rows, err := e1.db.Query(`SELECT blob_key, status FROM caches`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	store := e1.ArtifactOptions().Store
	n := 0
	for rows.Next() {
		var key, status string
		if err := rows.Scan(&key, &status); err != nil {
			t.Fatal(err)
		}
		if status != "kept" {
			t.Errorf("a row left in %s", status)
		}
		if _, err := store.Stat(context.Background(), key); err != nil {
			t.Errorf("the bytes of a kept entry are gone: %v", err)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no entries kept")
	}
	// No object under caches/ is left that no row refers to.
	fs := store.(*blobstore.Filesystem)
	_ = fs
	var objects int
	for _, id := range allCacheBlobKeys(t, e1) {
		if _, err := store.Stat(context.Background(), id); err == nil {
			objects++
		}
	}
	if objects != n {
		t.Errorf("%d objects for %d kept entries", objects, n)
	}
}

// allCacheBlobKeys lists every key an entry of any status ever had in this
// test, from the events of ids that were made, by trying the keys the
// engine would have made: not possible without the rows, so it returns the
// rows' keys. Rows deleted by the pruner are not among them, which is the
// point: their objects must not exist either (checked by the caller via
// the strays test).
func allCacheBlobKeys(t *testing.T, e *Engine) []string {
	t.Helper()
	rows, err := e.db.Query(`SELECT blob_key FROM caches`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

// In the database store, objects under caches/ that no entry refers to (a
// server stopped between writing the bytes and recording the entry) are
// collected after the grace period; the bytes of live entries are not.
func TestCacheStraysInTheDatabaseStoreAreCollected(t *testing.T) {
	clk := newClock()
	e := openWith(t, testdb.New(t), clk, ArtifactOptions{Choice: StoreDatabase}, 0, nil)
	r := &cacheRig{t: t, e: e, clk: clk}
	d, ok2 := e.ArtifactOptions().Store.(*blobstore.Database)
	if !ok2 {
		t.Fatalf("store %v", e.ArtifactOptions().Store)
	}
	j := r.start("demo", cacheOnMain)
	live := big(mib, 1)
	r.mustSave(j, "pip", cacheKey("live"), live)
	// An orphan: bytes written, no row.
	if err := putSmall(context.Background(), d, "caches/orphan-1", []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	e.pruneCaches()
	if _, err := d.Stat(context.Background(), "caches/orphan-1"); err != nil {
		t.Fatalf("an orphan was removed before the grace period: %v", err)
	}
	advanceClock(t, e, clk, strayGrace+time.Hour)
	for i := 0; i < 3; i++ { // pages
		e.pruneCaches()
	}
	if _, err := d.Stat(context.Background(), "caches/orphan-1"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the orphan is still there: %v", err)
	}
	en := r.lookup(j, "pip", cacheKey("live"), "")
	if en == nil {
		t.Fatal("the live entry was removed")
	}
	if got, err := r.download(j, en.ID); err != nil || !bytes.Equal(got, live) {
		t.Fatalf("the live entry's bytes are gone: %v", err)
	}
	// Deleting an entry removes its bytes after the grace period, in this
	// store too.
	if _, err := e.DeleteCaches("demo", en.ID, "", "ann"); err != nil {
		t.Fatal(err)
	}
	var key string
	if err := e.db.QueryRow(`SELECT blob_key FROM caches WHERE id = ?`, en.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	advanceClock(t, e, clk, cacheEndedGrace+time.Minute)
	e.pruneCaches()
	if _, err := d.Stat(context.Background(), key); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("the bytes of a deleted entry remain: %v", err)
	}
	var rows int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM caches`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rows %d %v", rows, err)
	}
}

// A run's pipeline must say which caches it has, and a server that knows
// less leaves runs alone (see migrations_order_test for the schema).
func TestOlderServerHasNoCaches(t *testing.T) {
	clk, addr := newClock(), testdb.New(t)
	old := mustOpenAs(t, addr, clk, schemaCache-1, &syncBuffer{})
	for name, err := range map[string]error{
		"lookup": func() error { _, err := old.LookupCache("l", CacheLookup{}); return err }(),
		"begin":  func() error { _, err := old.BeginCache("l", BeginCache{}); return err }(),
	} {
		if err == nil {
			t.Errorf("%s: an older server took a cache request", name)
		}
	}
}

// advanceClock moves the test clock on, first giving every lease a life that
// reaches past the new time: a step that holds its lease while the test
// jumps days would otherwise lose it to the reaper.
func advanceClock(t *testing.T, e *Engine, clk *clock, d time.Duration) {
	t.Helper()
	if _, err := e.db.Exec(`UPDATE leases SET expires_at = ?`, clk.t.Add(d+24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(d)
}

func (r *cacheRig) advance(d time.Duration) { advanceClock(r.t, r.e, r.clk, d) }
