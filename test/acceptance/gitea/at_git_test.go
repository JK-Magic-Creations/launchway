package giteaaccept

import (
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pushed makes a repository on the host holding the seeded history and
// returns its name and the seed working copy.
func (c *Config) pushed(t *testing.T) (name, seed string) {
	t.Helper()
	seed = c.seedRepo(t)
	name = c.newRepo(t)
	c.must(t, seed, "push", "-q", "--mirror", c.repoURL(name))
	// A host may register a push after it answers (Gitea does, through a queue): wait until the repository is no longer empty.
	c.waitBranch(t, name, "main")
	// The host picked the default branch when the repository was empty; point it at main now that it exists.
	if _, ok := eventually(30*time.Second, func() bool {
		var repo struct {
			Default string `json:"default_branch"`
		}
		if st, b, _ := c.do("GET", "/api/v1"+c.repoPath(name), nil, false); st == 200 {
			_ = json.Unmarshal(b, &repo)
		}
		if repo.Default == "main" {
			return true
		}
		c.do("PATCH", "/api/v1"+c.repoPath(name), map[string]any{"default_branch": "main"}, false) // "already in queue" is retried
		return false
	}); !ok {
		t.Fatalf("could not make main the default branch of %s", name)
	}
	return name, seed
}

// waitBranch waits until the API lists the branch: a host may take a moment to register a push.
func (c *Config) waitBranch(t *testing.T, name, branch string) {
	t.Helper()
	if _, ok := eventually(30*time.Second, func() bool {
		st, _, _ := c.do("GET", "/api/v1"+c.repoPath(name)+"/branches/"+branch, nil, false)
		return st == 200
	}); !ok {
		t.Fatalf("branch %s of %s never appeared in the API", branch, name)
	}
}

func (c *Config) clone(t *testing.T, url string, args ...string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "c")
	c.must(t, "", append(append([]string{"clone", "-q"}, args...), url, d)...)
	return d
}

func TestAT_HG_01_Import(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-01")
	name, seed := c.pushed(t)
	mirror := filepath.Join(t.TempDir(), "m.git")
	c.must(t, "", "clone", "-q", "--mirror", c.repoURL(name), mirror)
	wantRefs, wantObjs := c.fingerprint(t, seed)
	gotRefs, gotObjs := c.fingerprint(t, mirror)
	if wantRefs != gotRefs || wantObjs != gotObjs {
		t.Fatalf("clone differs from what was pushed\nrefs want:\n%s\ngot:\n%s\nobjects want %s got %s", wantRefs, gotRefs, wantObjs, gotObjs)
	}
	r.Saw(t, "%d refs and objects %s identical after push --mirror and clone --mirror", len(strings.Split(gotRefs, "\n")), gotObjs)
	c.must(t, mirror, "fsck", "--full", "--strict")
	r.Saw(t, "git fsck --full --strict clean")
	if out := c.must(t, seed, "push", "--mirror", c.repoURL(name)); !strings.Contains(out, "up-to-date") {
		t.Fatalf("second import was not a no-op: %s", out)
	}
	r.Saw(t, "second push --mirror changed nothing (Everything up-to-date)")
	r.NotAutomated(t, "the 44 real repositories (needs both hosts)")
}

func randomFile(t *testing.T, path string, mb int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := io.CopyN(f, rand.Reader, int64(mb)<<20); err != nil {
		t.Fatal(err)
	}
}

// bigPush pushes a branch holding mb megabytes of incompressible data.
func (c *Config) bigPush(t *testing.T, dir, url string, mb int) time.Duration {
	t.Helper()
	c.must(t, dir, "checkout", "-q", "-b", "big")
	randomFile(t, filepath.Join(dir, "big.bin"), mb)
	c.must(t, dir, "add", "-A")
	c.must(t, dir, "commit", "-q", "-m", "big")
	start := time.Now()
	c.must(t, dir, "push", "-q", url, "big")
	return time.Since(start)
}

func TestAT_HG_02_SmartHTTP(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-02")
	name, _ := c.pushed(t)
	url := c.repoURL(name)
	work := c.clone(t, url)
	r.Saw(t, "clone over %s", strings.TrimPrefix(url, c.URL))
	c.clone(t, strings.TrimSuffix(url, ".git"))
	r.Saw(t, "clone also works without .git: /%s/%s", c.User, name)
	id := c.commit(t, work, "a.txt", "one\n", "add a")
	c.must(t, work, "push", "-q", "origin", "main")
	other := c.clone(t, url)
	c.must(t, other, "fetch", "-q")
	if got := strings.TrimSpace(c.must(t, other, "rev-parse", "origin/main")); got != id {
		t.Fatalf("fetch saw %s, want %s", got, id)
	}
	r.Saw(t, "push then fetch from a second clone: origin/main = %.12s", id)
	c.commit(t, other, "b.txt", "two\n", "add b")
	c.must(t, other, "push", "-q", "origin", "main")
	c.commit(t, work, "c.txt", "three\n", "diverge")
	if out, err := c.git(work, true, "push", "origin", "main"); err == nil {
		t.Fatalf("non-fast-forward push was accepted: %s", out)
	}
	r.Saw(t, "non-fast-forward push refused")
	c.shallowPartialV2(t, r, url)
	d := c.clone(t, url)
	took := c.bigPush(t, d, url, c.BigMB)
	r.Saw(t, "%d MB push accepted in %s", c.BigMB, took.Round(time.Second))
	r.NotAutomated(t, "from a pod in argocd; force-with-lease refusal by a ref rule; the 100 MiB per-object message")
}

func (c *Config) shallowPartialV2(t *testing.T, r *Result, url string) {
	t.Helper()
	sh := c.clone(t, url, "--depth", "1")
	if strings.TrimSpace(c.must(t, sh, "rev-parse", "--is-shallow-repository")) != "true" {
		t.Fatal("shallow clone is not shallow")
	}
	r.Saw(t, "shallow clone --depth 1 is shallow")
	pc := c.clone(t, url, "--filter=blob:none", "--no-checkout")
	if !strings.Contains(c.must(t, pc, "config", "remote.origin.promisor"), "true") {
		t.Fatal("partial clone did not become a promisor clone: the host ignored --filter")
	}
	r.Saw(t, "partial clone --filter=blob:none succeeded")
	out, err := c.gitWith("", append(c.gitEnv(true), "GIT_TRACE_PACKET=1"), "ls-remote", url)
	if err != nil || !strings.Contains(out, "version 2") {
		t.Fatalf("protocol version 2 was not used (err %v)", err)
	}
	r.Saw(t, "protocol v2 negotiated")
}

func TestAT_HG_04_PullMirror(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-04")
	rc := c.startReceiverIfPossible(t)
	if rc != nil {
		if c.localPullMirror(t, r, rc) {
			return
		}
	}
	name := repoPrefix + randHex(4)
	st, body, err := c.do("POST", "/api/v1/repos/migrate", map[string]any{"clone_addr": c.MirrorSrc, "repo_name": name, "mirror": true,
		"mirror_interval": "10m0s", "private": true, "service": "git"}, false)
	if err != nil || st != 201 {
		t.Fatalf("pull mirror of %s: status %d %v %.300s", c.MirrorSrc, st, err, string(body))
	}
	c.repos = append(c.repos, name)
	t.Cleanup(func() { c.deleteRepo(t, name) })
	c.checkPullMirror(t, r, name, c.MirrorSrc)
	r.NotAutomated(t, "a commit pushed to GitHub appearing within 10 minutes (not allowed to push to GitHub); Argo CD application launchway-dev syncing from it")
}

// localPullMirror mirrors a repository on this machine; it returns false
// when the host refuses private addresses, so the caller can fall back to a
// public source.
func (c *Config) localPullMirror(t *testing.T, r *Result, rc *Receiver) bool {
	t.Helper()
	srcURL, srcDir := rc.GitRepo(c, t, "mirror-src")
	seed := c.seedRepo(t)
	c.must(t, seed, "push", "-q", "--mirror", srcDir)
	name := repoPrefix + randHex(4)
	st, body, _ := c.do("POST", "/api/v1/repos/migrate", map[string]any{"clone_addr": srcURL, "repo_name": name, "mirror": true,
		"mirror_interval": "10m0s", "private": true, "service": "git"}, false)
	if st != 201 {
		t.Logf("host refuses a private-address mirror source (status %d: %.160s): using the public source", st, string(body))
		return false
	}
	c.repos = append(c.repos, name)
	t.Cleanup(func() { c.deleteRepo(t, name) })
	c.checkPullMirror(t, r, name, srcURL)
	id := c.commit(t, seed, "later.txt", "later\n", "after the mirror")
	c.must(t, seed, "push", "-q", srcDir, "main")
	c.api(t, "POST", c.repoPath(name)+"/mirror-sync", nil, nil, 200)
	took, ok := eventually(time.Minute, func() bool {
		m, err := c.remoteRefs(c.repoURL(name), true, "")
		return err == nil && m["refs/heads/main"] == id
	})
	if !ok {
		t.Fatalf("new commit %.12s did not reach the mirror within a minute of mirror-sync", id)
	}
	r.Saw(t, "new commit on the source reached the mirror %s after mirror-sync", took.Round(time.Second))
	return true
}

// checkPullMirror checks the repository is a read-only mirror of src with
// the same branches.
func (c *Config) checkPullMirror(t *testing.T, r *Result, name, src string) {
	t.Helper()
	var repo struct {
		Mirror   bool   `json:"mirror"`
		Interval string `json:"mirror_interval"`
	}
	c.api(t, "GET", c.repoPath(name), nil, &repo, 200)
	if !repo.Mirror {
		t.Fatal("repository is not marked as a mirror")
	}
	r.Saw(t, "repository is a mirror, interval %q", repo.Interval)
	want, err := c.remoteRefs(src, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var diff string
	_, ok := eventually(2*time.Minute, func() bool {
		got, err := c.remoteRefs(c.repoURL(name), true, "")
		diff = ""
		for ref, id := range want {
			if strings.HasPrefix(ref, "refs/heads/") && got[ref] != id {
				diff = ref
			}
		}
		return err == nil && diff == "" && len(got) > 0
	})
	if !ok {
		t.Fatalf("mirror branch %s differs from the source", diff)
	}
	r.Saw(t, "every branch of the source (%d refs listed) is at the same commit in the mirror", len(want))
	work := c.clone(t, c.repoURL(name))
	c.commit(t, work, "x.txt", "x\n", "push to a mirror")
	if out, err := c.git(work, true, "push", "origin", "HEAD"); err == nil {
		t.Fatalf("push to a pull mirror was accepted: %s", out)
	}
	r.Saw(t, "push to the mirror is refused")
	c.api(t, "POST", c.repoPath(name)+"/mirror-sync", nil, nil, 200)
	r.Saw(t, "manual mirror-sync accepted")
}

// startReceiverIfPossible returns nil, with a log line, if no receiver can be started.
func (c *Config) startReceiverIfPossible(t *testing.T) *Receiver {
	t.Helper()
	if _, err := c.receiverHost(); err != nil {
		t.Log(err)
		return nil
	}
	return c.startReceiver(t)
}

func TestAT_HG_05_PushMirror(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-05")
	rc := c.startReceiver(t)
	dstURL, dstDir := rc.GitRepo(c, t, "mirror-dst")
	name, seed := c.pushed(t)
	st, body, _ := c.do("POST", "/api/v1"+c.repoPath(name)+"/push_mirrors", map[string]any{"remote_address": dstURL, "interval": "8h0m0s", "sync_on_commit": true}, false)
	if st != 200 && st != 201 {
		r.skip(t, "host refuses to create a push mirror to a private address ("+c.scrub(string(body))+")")
	}
	id := c.commit(t, seed, "pushed.txt", "p\n", "after the mirror")
	c.must(t, seed, "push", "-q", c.repoURL(name), "main")
	c.api(t, "POST", c.repoPath(name)+"/push_mirrors-sync", nil, nil, 200)
	took, ok := eventually(time.Minute, func() bool {
		out, _ := c.git(dstDir, false, "rev-parse", "refs/heads/main")
		return strings.TrimSpace(out) == id
	})
	if !ok {
		t.Fatalf("push mirror target did not get %.12s within a minute", id)
	}
	want, _ := c.remoteRefs(c.repoURL(name), true, "")
	got, _ := c.remoteRefs(dstURL, false, "")
	for ref, v := range want {
		if got[ref] != v {
			t.Fatalf("ref %s: host %.12s, mirror %.12s", ref, v, got[ref])
		}
	}
	r.Saw(t, "after a push, the target's %d refs equal the host's (new commit %.12s after %s)", len(want), id, took.Round(time.Second))
	r.NotAutomated(t, "41 repositories to GitHub with per-repository deploy keys; the broken-key alert within the hour")
}

func TestAT_HG_06_Archive(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-06")
	name, seed := c.pushed(t)
	url := c.repoURL(name)
	c.api(t, "PATCH", c.repoPath(name), map[string]any{"archived": true}, nil, 200)
	c.commit(t, seed, "late.txt", "late\n", "after archive")
	if out, err := c.git(seed, true, "push", url, "main"); err == nil {
		t.Fatalf("push to an archived repository was accepted: %s", out)
	}
	r.Saw(t, "push to the archived repository refused")
	c.clone(t, url)
	r.Saw(t, "clone of the archived repository works")
	var repo struct {
		Archived bool `json:"archived"`
	}
	c.api(t, "GET", c.repoPath(name), nil, &repo, 200)
	if !repo.Archived {
		t.Fatal("API does not report archived")
	}
	c.api(t, "PATCH", c.repoPath(name), map[string]any{"archived": false}, nil, 200)
	c.must(t, seed, "push", "-q", url, "main")
	r.Saw(t, "after unarchive, push accepted")
}

func TestAT_HG_08_ProtectedTags(t *testing.T) {
	c := host(t)
	r := begin(t, "AT-HG-08")
	name, seed := c.pushed(t)
	url := c.repoURL(name)
	c.must(t, seed, "tag", "v2.3")
	c.must(t, seed, "push", "-q", url, "v2.3")
	other := os.Getenv("LW_ACCEPT_OTHER_USER")
	if other == "" {
		r.skip(t, "Gitea needs an allowed-user list that does not include the pusher: set LW_ACCEPT_OTHER_USER to another existing user (Launchway's ref rules need no such user)")
	}
	c.api(t, "POST", c.repoPath(name)+"/tag_protections", map[string]any{"name_pattern": "v*", "whitelist_usernames": []string{other}}, nil, 200, 201)
	r.Saw(t, "tag protection v* created after tag v2.3 existed")
	moved := c.commit(t, seed, "m.txt", "m\n", "to move the tag")
	c.must(t, seed, "tag", "-f", "v2.3", moved)
	if out, err := c.git(seed, true, "push", "--force", url, "v2.3"); err == nil {
		t.Fatalf("force-moving the protected tag v2.3 was accepted: %s", out)
	}
	r.Saw(t, "force-moving v2.3 refused")
	if out, err := c.git(seed, true, "push", url, ":refs/tags/v2.3"); err == nil {
		t.Fatalf("deleting the protected tag v2.3 was accepted: %s", out)
	}
	r.Saw(t, "deleting v2.3 refused")
	r.NotAutomated(t, "the audit log showing the attempt (Launchway's audit API)")
}
