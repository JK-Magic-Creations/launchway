package api

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// A cache pipeline. The script finds what the cache restored, counts how
// many runs have added to it, and makes a file too big for one chunk the
// first time.
func cachePipelineText(name, fallback, save, extra string) string {
	return `
pipeline: demo
on:
  pull_request: {}
steps:
  test:
    image: none
    cache:
      - name: ` + name + `
        paths: [.cache/deps]
        key: { files: [requirements.txt], text: v1 }
        fallback: ` + fallback + `
        save: ` + save + `
    run: |
      mkdir -p .cache/deps
      if [ -f .cache/deps/n ]; then echo "found n=$(cat .cache/deps/n)"; else echo "found nothing"; echo 0 > .cache/deps/n; fi
      echo $(( $(cat .cache/deps/n) + 1 )) > .cache/deps/n
      [ -f .cache/deps/big ] || head -c 9000000 /dev/urandom > .cache/deps/big
      echo "now n=$(cat .cache/deps/n)"
` + extra
}

type cacheEnv struct {
	*env
	repo string
}

func newCacheEnv(t *testing.T) *cacheEnv {
	t.Helper()
	ce := newCacheEnvNoRunner(t)
	ce.runner(t, "r1")
	return ce
}

func newCacheEnvNoRunner(t *testing.T) *cacheEnv {
	t.Helper()
	e := setupStore(t)
	repo := filepath.Join(e.dir, "repo")
	os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	ce := &cacheEnv{env: e, repo: repo}
	ce.commit(t, "requirements.txt", "requests==1\n")
	return ce
}

// commit writes a file and commits it, returning the commit.
func (ce *cacheEnv) commit(t *testing.T, name, data string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(ce.repo, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, ce.repo, "add", "-A")
	gitIn(t, ce.repo, "commit", "-q", "-m", "change "+name)
	return gitIn(t, ce.repo, "rev-parse", "HEAD")
}

func (ce *cacheEnv) runWith(t *testing.T, project, text string, trig engine.Trigger) (*engine.RunDetail, string) {
	t.Helper()
	p, err := pipeline.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	r, err := ce.eng.CreateRun(engine.NewRun{Project: project, Pipeline: p, Trigger: trig, Source: engine.Source{Kind: "git", URL: ce.repo}})
	if err != nil {
		t.Fatal(err)
	}
	d := ce.wait(t, r.ID, 60*time.Second)
	lines, _, err := ce.eng.Logs(r.ID, "test", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.Text + "\n")
	}
	return d, b.String()
}

func (ce *cacheEnv) head(t *testing.T) string { return gitIn(t, ce.repo, "rev-parse", "HEAD") }

func pushMain(sha string) engine.Trigger {
	return engine.Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: sha}
}

func pullRequest(n int, sha string) engine.Trigger {
	return engine.Trigger{Event: "pull_request", Ref: engine.PullRequestRef(n), PullRequest: n, SHA: sha}
}

func cachesOf(t *testing.T, d *engine.RunDetail) []engine.CacheResult {
	t.Helper()
	for _, s := range d.StepList {
		if s.ID == "test" {
			return s.Caches
		}
	}
	t.Fatal("no step test")
	return nil
}

// The second run restores what the first saved, and says so; a changed lock
// file misses, and fallback latest restores the older entry; a pull request
// restores the default branch's entry and saves only into its own scope,
// which the default branch never reads; a failed step saves only with
// save: always.
func TestCacheRestoreAndSaveEndToEnd(t *testing.T) {
	ce := newCacheEnv(t)
	text := cachePipelineText("deps", "latest", "success", "")
	sha1 := ce.head(t)

	// First run: nothing to restore; the entry is saved, in two chunks.
	d, log := ce.runWith(t, "demo", text, pushMain(sha1))
	if d.Status != "succeeded" || !strings.Contains(log, "found nothing") || !strings.Contains(log, "Cache deps: no entry; the step runs without it") ||
		!strings.Contains(log, "Cache deps: saved ") || !strings.Contains(log, " as key ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	c := cachesOf(t, d)
	if len(c) != 1 || c[0].Result != engine.CacheSaved || c[0].Restored != engine.CacheMiss || c[0].Bytes < 9_000_000 || len(c[0].Key) != 64 || c[0].Scope != "default" {
		t.Fatalf("%+v", c)
	}
	key1 := c[0].Key

	// Second run, same lock file: restored whole, and nothing is saved.
	d, log = ce.runWith(t, "demo", text, pushMain(sha1))
	if d.Status != "succeeded" || !strings.Contains(log, "found n=1") || !strings.Contains(log, "now n=2") ||
		!strings.Contains(log, "Cache deps: restored key "+key1[:4]+"… (exact, ") || strings.Contains(log, "Cache deps: saved") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	if c := cachesOf(t, d); len(c) != 1 || c[0].Result != engine.CacheHit || c[0].Key != key1 || c[0].Bytes < 9_000_000 {
		t.Fatalf("%+v", c)
	}

	// A changed lock file is another key: with fallback latest the older entry
	// is restored, and the result saved under the new key.
	sha2 := ce.commit(t, "requirements.txt", "requests==2\n")
	d, log = ce.runWith(t, "demo", text, pushMain(sha2))
	if d.Status != "succeeded" || !strings.Contains(log, "found n=1") || !strings.Contains(log, "restored key "+key1[:4]+"… (fallback") ||
		!strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	c = cachesOf(t, d)
	if len(c) != 1 || c[0].Result != engine.CacheSaved || c[0].Restored != engine.CacheFallback || c[0].Key == key1 {
		t.Fatalf("%+v", c)
	}
	key2 := c[0].Key

	// Without the fallback the changed lock file starts from nothing.
	sha3 := ce.commit(t, "requirements.txt", "requests==3\n")
	d, log = ce.runWith(t, "demo", cachePipelineText("deps", "none", "success", ""), pushMain(sha3))
	if !strings.Contains(log, "found nothing") || !strings.Contains(log, "Cache deps: no entry; the step runs without it") {
		t.Fatalf("%s", log)
	}

	// A pull request, on a new lock file: restores the default scope's newest
	// entry (fallback) and saves into pull:5 only.
	sha4 := ce.commit(t, "requirements.txt", "requests==4-pr\n")
	d, log = ce.runWith(t, "demo", text, pullRequest(5, sha4))
	if d.Status != "succeeded" || !strings.Contains(log, "restored key ") || !strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	prKey := cachesOf(t, d)[0].Key
	if r := cachesOf(t, d)[0]; r.Scope != "pull:5" || r.Restored != engine.CacheFallback {
		t.Fatalf("%+v", r)
	}
	list, err := ce.eng.Caches("demo")
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, v := range list.Entries {
		scopes[v.Key] = v.Scope
	}
	if scopes[key1] != "default" || scopes[key2] != "default" || scopes[prKey] != "pull:5" {
		t.Fatalf("%v", scopes)
	}
	// The default branch on the same lock file does not see the pull
	// request's entry, even with the fallback: it restores its own.
	d, log = ce.runWith(t, "demo", cachePipelineText("deps", "none", "success", ""), pushMain(sha4))
	if !strings.Contains(log, "Cache deps: no entry; the step runs without it") || !strings.Contains(log, "found nothing") {
		t.Fatalf("POISONING: a default-branch run read what a pull request saved:\n%s", log)
	}
	if r := cachesOf(t, d)[0]; r.Result != engine.CacheSaved || r.Scope != "default" || r.Key != prKey {
		t.Fatalf("the default branch saves its own entry under the same key: %+v", r)
	}
	// And a pull request does not overwrite the default scope's entry: both
	// exist, each with its own bytes.
	list, _ = ce.eng.Caches("demo")
	n := 0
	for _, v := range list.Entries {
		if v.Key == prKey {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("an entry per scope under the key: %d", n)
	}

	// A failed step saves nothing by default, and with save: always it does.
	fail := "      exit 1\n"
	sha5 := ce.commit(t, "requirements.txt", "requests==5\n")
	d, log = ce.runWith(t, "demo", cachePipelineText("deps", "none", "success", fail), pushMain(sha5))
	if d.Status != "failed" || strings.Contains(log, "Cache deps: saved") || strings.Contains(log, "not saved") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	if c := cachesOf(t, d); len(c) != 1 || c[0].Result != engine.CacheMiss {
		t.Fatalf("%+v", c)
	}
	d, log = ce.runWith(t, "demo", cachePipelineText("deps", "none", "always", fail), pushMain(sha5))
	if d.Status != "failed" || !strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	if c := cachesOf(t, d); len(c) != 1 || c[0].Result != engine.CacheSaved {
		t.Fatalf("%+v", c)
	}
	// save: never.
	sha6 := ce.commit(t, "requirements.txt", "requests==6\n")
	d, log = ce.runWith(t, "demo", cachePipelineText("deps", "none", "never", ""), pushMain(sha6))
	if d.Status != "succeeded" || strings.Contains(log, "Cache deps: saved") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	// Another project does not see any of it.
	d, log = ce.runWith(t, "other", text, pushMain(sha1))
	if !strings.Contains(log, "Cache deps: no entry; the step runs without it") || !strings.Contains(log, "found nothing") {
		t.Fatalf("another project read this one's cache:\n%s", log)
	}
	if got := cachesOf(t, d); got[0].Scope != "default" {
		t.Fatalf("%+v", got)
	}
}

// Draft runs and caching off: the step runs, says why, and passes.
func TestCachePolicyAndDraftsEndToEnd(t *testing.T) {
	ce := newCacheEnv(t)
	text := cachePipelineText("deps", "latest", "success", "")
	sha := ce.head(t)
	if d, _ := ce.runWith(t, "demo", text, pushMain(sha)); d.Status != "succeeded" {
		t.Fatal(d.Status)
	}
	// A draft run restores the default branch's entry and saves nothing.
	p, _ := pipeline.Parse([]byte(text))
	r, err := ce.eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p, Trigger: pushMain(sha), Source: engine.Source{Kind: "git", URL: ce.repo},
		Draft: &engine.Draft{Text: text, Hash: "sha256:x"}})
	if err != nil {
		t.Fatal(err)
	}
	d := ce.wait(t, r.ID, 60*time.Second)
	lines, _, _ := ce.eng.Logs(r.ID, "test", 0, 0, 0)
	var log strings.Builder
	for _, l := range lines {
		log.WriteString(l.Text + "\n")
	}
	if d.Status != "succeeded" || !strings.Contains(log.String(), "restored key ") || strings.Contains(log.String(), "Cache deps: saved") {
		t.Fatalf("%s\n%s", d.Status, log.String())
	}
	// Caching off for the project: a miss that says why, a refusal to save,
	// and a step that passes all the same.
	if _, _, err := ce.eng.SetCachePolicy("demo", engine.CachePolicy{Enabled: false, MaxProjectGiB: 1, MaxEntryGiB: 1, IdleDays: 7}, "ann"); err != nil {
		t.Fatal(err)
	}
	sha2 := ce.commit(t, "requirements.txt", "requests==9\n")
	d, l := ce.runWith(t, "demo", text, pushMain(sha2))
	if d.Status != "succeeded" || !strings.Contains(l, "no entry (caching is turned off for this project") || !strings.Contains(l, "Cache deps: not saved: caching is turned off") {
		t.Fatalf("%s\n%s", d.Status, l)
	}
	if c := cachesOf(t, d); c[0].Result != engine.CacheNotSaved || !strings.Contains(c[0].Reason, "turned off") {
		t.Fatalf("%+v", c)
	}
	// Too large for the entry limit: refused before it is sent, in words.
	if _, _, err := ce.eng.SetCachePolicy("demo", engine.CachePolicy{Enabled: true, MaxProjectGiB: 0.01, MaxEntryGiB: 0.005, IdleDays: 7}, "ann"); err != nil {
		t.Fatal(err)
	}
	sha3 := ce.commit(t, "requirements.txt", "requests==10\n")
	d, l = ce.runWith(t, "demo", cachePipelineText("deps", "none", "success", ""), pushMain(sha3))
	if d.Status != "succeeded" || !strings.Contains(l, "Cache deps: not saved: this cache is ") || !strings.Contains(l, "an entry may hold at most 5.1 MiB") {
		t.Fatalf("%s\n%s", d.Status, l)
	}
}

// What a restore must not do whatever the store holds: write outside the
// workspace, follow a link, take a file the step did not ask to keep. A
// damaged download fails the restore and not the step.
func TestHostileCacheEntriesNeverReachOutsideTheWorkspace(t *testing.T) {
	ce := newCacheEnvNoRunner(t)
	outside := filepath.Join(ce.dir, "outside")
	os.MkdirAll(outside, 0o755)
	platform := runtime.GOOS + "/" + runtime.GOARCH

	archive := func(members func(tw *tar.Writer)) []byte {
		var raw bytes.Buffer
		tw := tar.NewWriter(&raw)
		members(tw)
		tw.Close()
		var out bytes.Buffer
		enc, _ := zstd.NewWriter(&out)
		enc.Write(raw.Bytes())
		enc.Close()
		return out.Bytes()
	}
	reg := func(tw *tar.Writer, name, data string) {
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data)), Format: tar.FormatPAX})
		tw.Write([]byte(data))
	}
	cases := map[string][]byte{
		"parent": archive(func(tw *tar.Writer) { reg(tw, "../outside/pwned", "x"); reg(tw, ".cache/deps/ok", "fine") }),
		"absolute": archive(func(tw *tar.Writer) {
			reg(tw, filepath.Join(outside, "pwned"), "x")
		}),
		"symlink": archive(func(tw *tar.Writer) {
			tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: ".cache/deps/l", Linkname: outside, Mode: 0o777})
			reg(tw, ".cache/deps/l/pwned", "x")
		}),
		"not asked for": archive(func(tw *tar.Writer) {
			reg(tw, ".cache/deps/ok", "fine")
			reg(tw, "evil.sh", "echo pwned > "+filepath.Join(outside, "pwned"))
		}),
		"bomb": archive(func(tw *tar.Writer) { reg(tw, ".cache/deps/zeros", string(make([]byte, 300<<20))) }),
	}
	// Plant every entry first, before a runner is there to take the planting
	// run's step, then run each case.
	type planted struct{ name, sha string }
	var plantedCases []planted
	keyed := cachePipelineText("deps", "none", "success", "")
	p, _ := pipeline.Parse([]byte(keyed))
	for name, data := range cases {
		sha := ce.commit(t, "requirements.txt", "requests=="+name+"\n")
		key := executor.CacheKey(platform, "deps", "v1", stagedKey(t, ce, "requirements.txt"))
		if err := plant(t, ce.env, p, key, platform, data); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		plantedCases = append(plantedCases, planted{name, sha})
	}
	ce.runner(t, "r1")
	for _, pc := range plantedCases {
		name, sha := pc.name, pc.sha
		d, log := ce.runWith(t, "demo", keyed+"      test -f .cache/deps/ok && echo ok-restored || echo ok-absent\n      test ! -e evil.sh || echo EVIL-PRESENT\n", pushMain(sha))
		if d.Status != "succeeded" {
			t.Errorf("%s: a hostile entry failed the step: %s\n%s", name, d.Status, log)
		}
		if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
			t.Fatalf("%s: a file was written outside the workspace", name)
		}
		if strings.Contains(log, "EVIL-PRESENT") {
			t.Errorf("%s: a file the step did not ask to keep was restored", name)
		}
		switch name {
		case "not asked for":
			if !strings.Contains(log, "ok-restored") || !strings.Contains(log, "1 files in the entry are not under the cache's paths now and were left out") {
				t.Errorf("%s:\n%s", name, log)
			}
		default:
			if !strings.Contains(log, "Cache deps: not restored: the cache entry cannot be restored") || !strings.Contains(log, "the step runs without it") {
				t.Errorf("%s: the restore should fail in words:\n%s", name, log)
			}
		}
	}
}

// stagedKey stages the repository's file the way the runner does, so that a
// test can work out the key a run will look for.
func stagedKey(t *testing.T, ce *cacheEnv, name string) []executor.StagedFile {
	t.Helper()
	dir := t.TempDir()
	// The files are in the repository's working tree, which is what each
	// run checks out.
	staged, err := executor.Stage(context.Background(), &executor.Host{}, executor.Spec{Workspace: ce.repo}, []string{name},
		executor.StageLimits{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	return staged.Files
}

// plant stores bytes as the default branch's entry for a key, as a run that
// could write the store would.
func plant(t *testing.T, e *env, p *pipeline.Pipeline, key, platform string, data []byte) error {
	t.Helper()
	run, err := e.eng.CreateRun(engine.NewRun{Project: "demo", Pipeline: p, Trigger: pushMain("planted"), Source: engine.Source{Kind: "dir", Path: e.dir}})
	if err != nil {
		return err
	}
	_ = run
	// The step is leased by hand, so that no runner runs it.
	j, err := e.eng.Lease(context.Background(), engine.RunnerInfo{ID: "planter", Name: "planter", Features: engine.FeatureArtifacts + " " + engine.FeatureCache}, 0)
	if err != nil || j == nil {
		return err
	}
	sum := sha256.Sum256(data)
	b, err := e.eng.BeginCache(j.LeaseID, engine.BeginCache{Name: "deps", Key: key, Platform: platform, Bytes: int64(len(data)), Files: 1, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	for off := int64(0); off < int64(len(data)); {
		end := min(off+engine.ArtifactChunkBytes, int64(len(data)))
		c := sha256.Sum256(data[off:end])
		got, err := e.eng.PutCacheChunk(context.Background(), j.LeaseID, b.ID, off, hex.EncodeToString(c[:]), data[off:end])
		if err != nil {
			return err
		}
		off = got
	}
	if _, err = e.eng.CommitCache(context.Background(), j.LeaseID, b.ID); err != nil {
		return err
	}
	zero := 0
	return e.eng.Finish(j.LeaseID, engine.Result{Outcome: "succeeded", ExitCode: &zero})
}

// A download that does not match its SHA-256 fails the restore, not the
// step: the step runs without the cache and says so.
func TestCorruptedCacheDownloadFailsTheRestoreNotTheStep(t *testing.T) {
	ce := newCacheEnv(t)
	text := cachePipelineText("deps", "none", "success", "")
	sha := ce.head(t)
	d, log := ce.runWith(t, "demo", text, pushMain(sha))
	if d.Status != "succeeded" || !strings.Contains(log, "Cache deps: saved") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	size := cachesOf(t, d)[0].Bytes
	// Flip a byte in the middle of the stored archive.
	flipped := 0
	filepath.Walk(filepath.Join(ce.dir, "store"), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && info.Size() == size {
			b, _ := os.ReadFile(p)
			b[len(b)/2] ^= 0xff
			os.WriteFile(p, b, 0o644)
			flipped++
		}
		return nil
	})
	if flipped != 1 {
		t.Fatalf("found %d stored archives of %d bytes", flipped, size)
	}
	d, log = ce.runWith(t, "demo", text, pushMain(sha))
	if d.Status != "succeeded" || !strings.Contains(log, "Cache deps: not restored: ") || !strings.Contains(log, "corrupted") ||
		!strings.Contains(log, "found nothing") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	// The damaged entry was ended and this run saved a good one in its place,
	// which the next run restores.
	if !strings.Contains(log, "the damaged entry was ended") || !strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s", log)
	}
	if c := cachesOf(t, d); c[0].Result != engine.CacheSaved || c[0].Restored != engine.CacheMiss {
		t.Fatalf("%+v", c)
	}
	d, log = ce.runWith(t, "demo", text, pushMain(sha))
	if d.Status != "succeeded" || !strings.Contains(log, "found n=1") || !strings.Contains(log, "(exact, ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
}

// The same on Kubernetes, where the workspace is an emptyDir in the run's
// pod: the key's files are read, and the cache is saved and restored, through
// the helper container.
func TestCacheEndToEndOnKubernetes(t *testing.T) {
	ctxName := os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT")
	if ctxName == "" {
		t.Skip("set LAUNCHWAY_TEST_KUBE_CONTEXT to run Kubernetes tests")
	}
	ex, err := executor.NewKubernetes(executor.KubernetesOptions{Context: ctxName, Namespace: kubeTestNamespace(), StartTimeout: "5m"},
		"test-cache-"+strings.ReplaceAll(time.Now().Format("150405.000"), ".", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	ce := newCacheEnvNoRunner(t)
	r := ce.newRunner("kube")
	r.Exec = ex
	ce.startRunner(t, r)
	const alpine = "docker.io/library/alpine:3.22"
	text := strings.Replace(cachePipelineText("deps", "latest", "success", ""), "image: none", "image: "+alpine, 1)
	sha := ce.head(t)
	d, log := ce.runWith(t, "demo", text, pushMain(sha))
	if d.Status != "succeeded" || !strings.Contains(log, "found nothing") || !strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	d, log = ce.runWith(t, "demo", text, pushMain(sha))
	if d.Status != "succeeded" || !strings.Contains(log, "found n=1") || !strings.Contains(log, "(exact, ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	// A new lock file: the older entry is restored, the new one saved.
	sha2 := ce.commit(t, "requirements.txt", "requests==2\n")
	d, log = ce.runWith(t, "demo", text, pushMain(sha2))
	if d.Status != "succeeded" || !strings.Contains(log, "(fallback: ") || !strings.Contains(log, "Cache deps: saved ") {
		t.Fatalf("%s\n%s", d.Status, log)
	}
	_ = ex.Cleanup(context.Background(), func(context.Context, []string) (map[string]executor.RunInfo, error) { return nil, nil })
}
