package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/hosting"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// These tests run the real git client against the real server.

func git(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "credential.helper=", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = testlib.GitEnv(t, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// workingCopy makes a working copy with one commit.
func workingCopy(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	mustGit(t, d, "init", "-q")
	commit(t, d, "a.txt", "one\n", "first")
	return d
}

func commit(t *testing.T, dir, file, content, msg string) string {
	t.Helper()
	p := filepath.Join(dir, file)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", msg)
	return strings.TrimSpace(mustGit(t, dir, "rev-parse", "HEAD"))
}

// withToken is a repository address that carries a token as the password.
func withToken(base, path, token string) string {
	return strings.Replace(base, "http://", "http://git:"+token+"@", 1) + path
}

func hostedRepo(t *testing.T, e *authEnv, path string) forge.Repository {
	t.Helper()
	owner, name, _ := strings.Cut(path, "/")
	r, err := e.api.Forge.Provider.Repositories().Create(t.Context(), forge.NewRepository{Home: owner, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGitCloneFetchPushWithAToken(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "git")
	src := workingCopy(t)
	for i, form := range []string{"/git/acme/shop.git", "/acme/shop.git", "/acme/shop"} {
		u := withToken(e.srv.URL, form, tok)
		if i == 0 {
			mustGit(t, src, "push", "-q", u, "HEAD:refs/heads/main")
		}
		c := t.TempDir()
		mustGit(t, c, "clone", "-q", u, "c")
		if got := strings.TrimSpace(mustGit(t, filepath.Join(c, "c"), "log", "-1", "--format=%s")); got != "first" {
			t.Fatalf("%s: cloned %q", form, got)
		}
	}
	// A second clone fetches what the first pushes, over the Gitea-style address.
	u := withToken(e.srv.URL, "/acme/shop.git", tok)
	a, b := t.TempDir(), t.TempDir()
	mustGit(t, a, "clone", "-q", u, "a")
	mustGit(t, b, "clone", "-q", u, "b")
	id := commit(t, filepath.Join(a, "a"), "b.txt", "two\n", "second")
	mustGit(t, filepath.Join(a, "a"), "push", "-q", "origin", "main")
	mustGit(t, filepath.Join(b, "b"), "fetch", "-q")
	if got := strings.TrimSpace(mustGit(t, filepath.Join(b, "b"), "rev-parse", "origin/main")); got != id {
		t.Fatalf("fetch saw %s, want %s", got, id)
	}
	// A push that is not a fast-forward is refused by the server.
	commit(t, filepath.Join(b, "b"), "c.txt", "x\n", "diverge")
	if out, err := git(t, filepath.Join(b, "b"), "push", "origin", "main"); err == nil {
		t.Errorf("a non-fast-forward push was accepted:\n%s", out)
	}
	// Protocol version 2 is used for fetch.
	out := mustGit(t, b, "-c", "protocol.version=2", "ls-remote", u)
	_ = out
	cmd := exec.Command("git", "-c", "credential.helper=", "-c", "protocol.version=2", "ls-remote", u)
	cmd.Env = testlib.GitEnv(t, "GIT_TRACE_PACKET=1")
	trace, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(trace), "version 2") {
		t.Errorf("protocol v2 was not used: %v", err)
	}
	// The push was audited with its refs and the token's name.
	log, _ := e.eng.AuditLog(engine.AuditFilter{Action: "git.push", Limit: 20})
	if len(log) < 2 || log[0].Outcome != "ok" || !strings.Contains(log[len(log)-1].Detail, "refs/heads/main") || !strings.Contains(log[0].Detail, "token git") {
		t.Errorf("audit: %+v", log)
	}
}

// A repository the token may not read, and one that does not exist, answer alike.
func TestGitTokenOutsideItsScopeIsRefusedLikeAMissingRepository(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "other/secret")
	vic, nobody := e.tokenFor(t, "vic", "v"), e.tokenFor(t, "nobody", "n")
	probe := func(tok, path string) (int, string) {
		req, _ := http.NewRequest("GET", e.srv.URL+path+"/info/refs?service=git-upload-pack", nil)
		req.SetBasicAuth("x", tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	if c, _ := probe(vic, "/acme/shop.git"); c != 200 {
		t.Errorf("a viewer of acme/* cannot read acme/shop: %d", c)
	}
	c1, b1 := probe(vic, "/other/secret.git")
	c2, b2 := probe(vic, "/other/missing.git")
	c3, b3 := probe(nobody, "/acme/shop.git")
	if c1 != 404 || c1 != c2 || b1 != b2 || c3 != 404 || b3 != b1 {
		t.Errorf("unreadable %d %q, missing %d %q, no binding %d %q", c1, b1, c2, b2, c3, b3)
	}
	// A viewer reads but cannot push: 403 and an audit entry.
	src := workingCopy(t)
	if out, err := git(t, src, "push", withToken(e.srv.URL, "/acme/shop.git", vic), "HEAD:refs/heads/main"); err == nil || !strings.Contains(out, "403") {
		t.Errorf("a viewer pushed: %v\n%s", err, out)
	}
	log, _ := e.eng.AuditLog(engine.AuditFilter{Action: "git.push", Limit: 5})
	if len(log) == 0 || log[0].Outcome != "denied" {
		t.Errorf("a refused push is not audited: %+v", log)
	}
}

func TestGitCredentialRules(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	ada := e.signIn(t, "ada")
	id, tok := ada.newToken(e.srv.URL, "rules")
	adv := "/acme/shop.git/info/refs?service=git-upload-pack"
	get := func(url string, h ...string) *http.Response {
		req, _ := http.NewRequest("GET", url, nil)
		for i := 0; i+1 < len(h); i += 2 {
			req.Header.Set(h[i], h[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	basic := func(tk string) string {
		r, _ := http.NewRequest("GET", "http://x", nil)
		r.SetBasicAuth("u", tk)
		return r.Header.Get("Authorization")
	}
	// 401 with a Basic challenge, and the token never echoed.
	if r := get(e.srv.URL + adv); r.StatusCode != 401 || !strings.HasPrefix(r.Header.Get("WWW-Authenticate"), "Basic") {
		t.Errorf("anonymous on a private repository: %d %q", r.StatusCode, r.Header.Get("WWW-Authenticate"))
	}
	if r := get(e.srv.URL+adv, "Authorization", basic("lw_"+strings.Repeat("x", 32))); r.StatusCode != 401 {
		t.Errorf("a wrong token: %d", r.StatusCode)
	}
	for _, h := range [][]string{{"Authorization", basic(tok)}, {"Authorization", "Bearer " + tok}, {"Authorization", "token " + tok}} {
		if r := get(e.srv.URL+adv, h...); r.StatusCode != 200 {
			t.Errorf("%s: %d", h[1][:6], r.StatusCode)
		}
	}
	// A token in the query string is refused, and a session cookie is not a Git credential.
	if r := get(e.srv.URL + adv + "&token=" + tok); r.StatusCode != 400 {
		t.Errorf("token in the query: %d", r.StatusCode)
	}
	if r := get(e.srv.URL+adv, "Cookie", sessionCookie+"="+ada.cookieValue(e.srv.URL, sessionCookie)); r.StatusCode != 401 {
		t.Errorf("a session cookie: %d", r.StatusCode)
	}
	// Basic over plain HTTP is refused unless the listener is loopback or the operator allows it.
	remote := func(allow bool) int {
		e.api.Forge.AllowInsecureHTTP = allow
		req := httptest.NewRequest("GET", adv, nil)
		req.SetBasicAuth("u", tok)
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 80}))
		rec := httptest.NewRecorder()
		e.srv.Config.Handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := remote(false); c != 403 {
		t.Errorf("Basic over plain HTTP to a public address: %d", c)
	}
	if c := remote(true); c != 200 {
		t.Errorf("with allow_insecure_http: %d", c)
	}
	e.api.Forge.AllowInsecureHTTP = false
	// Revoking a token takes effect on the next request.
	if r := ada.do("DELETE", e.srv.URL+"/api/v1/tokens/"+id, ""); r.status != 204 {
		t.Fatalf("revoke: %d", r.status)
	}
	if r := get(e.srv.URL+adv, "Authorization", basic(tok)); r.StatusCode != 401 {
		t.Errorf("a revoked token: %d", r.StatusCode)
	}
}

func TestGitAnonymousReadOnlyWhenPublic(t *testing.T) {
	e, h := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "git")
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/shop.git", tok), "HEAD:refs/heads/main")
	anon := e.srv.URL + "/acme/shop.git"
	if out, err := git(t, t.TempDir(), "clone", "-q", anon, "x"); err == nil {
		t.Fatalf("an anonymous clone of a private repository worked:\n%s", out)
	}
	missing, _ := git(t, t.TempDir(), "clone", "-q", e.srv.URL+"/acme/nothing.git", "x")
	private, _ := git(t, t.TempDir(), "clone", "-q", anon, "x")
	if strings.ReplaceAll(missing, "nothing", "shop") != private {
		t.Errorf("anonymous answers differ:\n%s\n%s", missing, private)
	}
	if err := h.Provider.SetVisibility(t.Context(), "acme/shop", forge.Public); err != nil {
		t.Fatal(err)
	}
	mustGit(t, t.TempDir(), "clone", "-q", anon, "x")
	if out, err := git(t, src, "push", anon, "HEAD:refs/heads/other"); err == nil {
		t.Errorf("an anonymous push worked:\n%s", out)
	}
}

func TestGitRunTokenReadsOneRepositoryOnly(t *testing.T) {
	e, h := hostedEnv(t, envOpts{})
	shop, other := hostedRepo(t, e, "acme/shop"), hostedRepo(t, e, "acme/other")
	ada := e.tokenFor(t, "ada", "git")
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/shop.git", ada), "HEAD:refs/heads/main")
	run, err := h.RunTokens.MintForRun(shop.ID, "run_1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mustGit(t, t.TempDir(), "clone", "-q", withToken(e.srv.URL, "/acme/shop.git", run), "x")
	if _, err := git(t, t.TempDir(), "clone", "-q", withToken(e.srv.URL, "/acme/other.git", run), "x"); err == nil {
		t.Error("a run's token read another repository")
	}
	if out, err := git(t, src, "push", withToken(e.srv.URL, "/acme/shop.git", run), "HEAD:refs/heads/x"); err == nil || !strings.Contains(out, "403") {
		t.Errorf("a run's token pushed: %v\n%s", err, out)
	}
	expired, _ := githttp.RunTokens{Key: h.RunTokens.Key, Now: func() time.Time { return time.Now().Add(-time.Hour) }}.MintForRun(shop.ID, "run_2", time.Minute)
	if _, err := git(t, t.TempDir(), "clone", "-q", withToken(e.srv.URL, "/acme/shop.git", expired), "x"); err == nil {
		t.Error("an expired run token worked")
	}
	if _, err := git(t, t.TempDir(), "clone", "-q", withToken(e.srv.URL, "/acme/shop.git", run[:len(run)-2]+"xx"), "x"); err == nil {
		t.Error("a forged run token worked")
	}
	_ = other
}

func TestGitHiddenRefsAndUnreachableObjects(t *testing.T) {
	e, h := hostedEnv(t, envOpts{})
	repo := hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "git")
	u := withToken(e.srv.URL, "/acme/shop.git", tok)
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", u, "HEAD:refs/heads/main")
	// The reserved namespace cannot be pushed to, and is not advertised.
	if out, err := git(t, src, "push", u, "HEAD:refs/launchway/evil"); err == nil {
		t.Errorf("a push to the reserved namespace worked:\n%s", out)
	}
	if refs := mustGit(t, src, "ls-remote", u); strings.Contains(refs, "refs/launchway") {
		t.Errorf("a reserved ref is advertised:\n%s", refs)
	}
	// An object no ref reaches cannot be fetched by its ID: allowAnySHA1InWant is off.
	mustGit(t, src, "checkout", "-q", "-b", "temp")
	secret := commit(t, src, "secret.txt", "s3cret\n", "secret")
	mustGit(t, src, "push", "-q", u, "temp")
	mustGit(t, src, "push", "-q", u, ":temp")
	fetch := func(version string) error {
		c := t.TempDir()
		mustGit(t, c, "init", "-q")
		out, err := git(t, c, "-c", "protocol.version="+version, "fetch", u, secret)
		if err == nil {
			t.Logf("protocol %s fetched the unreachable commit:\n%s", version, out)
		}
		return err
	}
	if fetch("0") == nil || fetch("1") == nil {
		t.Error("an unreachable commit was fetched by its ID over protocol 0 or 1")
	}
	// Protocol version 2 does not look at what is advertised: git serves any object it has. What
	// keeps a removed secret from being fetched is that consolidation drops what no ref reaches.
	if _, err := h.Store.Maintain(t.Context(), repo.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := h.Store.Wipe(repo.ID); err != nil {
		t.Fatal(err)
	}
	if fetch("2") == nil {
		t.Error("an unreachable commit was fetched over protocol 2 after consolidation")
	}
}

func TestGitLimits(t *testing.T) {
	e, _ := hostedEnvWith(t, envOpts{}, hosting.Options{MaxPackBytes: 1 << 20}, githttp.Options{MaxPushBytes: 8 << 20, MaxFetchBytes: 1 << 20})
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "git")
	u := withToken(e.srv.URL, "/acme/shop.git", tok)
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", u, "HEAD:refs/heads/main")
	pushed := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	// An object over the limit is refused with a message from the remote.
	big := make([]byte, 3<<20)
	_, _ = rand.Read(big)
	mustGit(t, src, "checkout", "-q", "-b", "big")
	if err := os.WriteFile(filepath.Join(src, "big.bin"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-q", "-m", "big")
	out, err := git(t, src, "push", u, "big")
	if err == nil || !strings.Contains(out, "remote:") {
		t.Errorf("an oversize push: %v\n%s", err, out)
	}
	// A gzip request that inflates past the limit is refused, not decompressed into memory.
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	line := fmt.Sprintf("%04xwant %s\n", len("want ")+40+1+4, pushed) // valid, so git reads on
	for range 16 << 20 / len(line) {
		_, _ = zw.Write([]byte(line))
	}
	zw.Close()
	req, _ := http.NewRequest("POST", u+"/git-upload-pack", &z)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Errorf("a pack bomb: %d", resp.StatusCode)
	}
	// A body over the declared limit is refused before it is read.
	req, _ = http.NewRequest("POST", u+"/git-receive-pack", bytes.NewReader(make([]byte, 9<<20)))
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Errorf("an oversize push body: %d", resp.StatusCode)
	}
	// The wrong content type and the dumb protocol are refused.
	req, _ = http.NewRequest("POST", u+"/git-upload-pack", strings.NewReader("x"))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Errorf("no content type: %d", resp.StatusCode)
	}
	resp, _ = http.Get(u + "/info/refs")
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("the dumb protocol: %d", resp.StatusCode)
	}
}

func TestGitGzipRequestAndLargeCloneAndDeepPaths(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	repo := hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "git")
	u := withToken(e.srv.URL, "/acme/shop.git", tok)
	src := workingCopy(t)
	deep := strings.Repeat("d/", 150) + "file.txt"
	commit(t, src, deep, "deep\n", "deep")
	blob := make([]byte, 24<<20)
	_, _ = rand.Read(blob)
	if err := os.WriteFile(filepath.Join(src, "large.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-q", "-m", "large")
	mustGit(t, src, "push", "-q", u, "HEAD:refs/heads/main")
	c := t.TempDir()
	mustGit(t, c, "clone", "-q", u, "c")
	if got, _ := os.ReadFile(filepath.Join(c, "c", "large.bin")); !bytes.Equal(got, blob) {
		t.Error("the large file differs after a clone")
	}
	if got, _ := os.ReadFile(filepath.Join(c, "c", deep)); string(got) != "deep\n" {
		t.Error("the deeply nested file differs")
	}
	mustGit(t, filepath.Join(c, "c"), "fsck", "--strict")
	// A fetch request sent gzip-compressed is understood.
	head := strings.TrimSpace(mustGit(t, src, "rev-parse", "HEAD"))
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	_, _ = zw.Write([]byte(pkt("want "+head+" ofs-delta\n") + "0000" + pkt("done\n")))
	zw.Close()
	req, _ := http.NewRequest("POST", e.srv.URL+"/acme/shop.git/git-upload-pack", &z)
	req.SetBasicAuth("u", tok)
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	_, _ = body.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(body.Bytes(), []byte("PACK")) {
		t.Errorf("gzip fetch: %d (%d bytes)", resp.StatusCode, body.Len())
	}
	_ = repo
}

func TestGiteaStyleAddressRedirectsABrowserToTheRepository(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "other/secret")
	vic := e.signIn(t, "vic")
	html := []string{"Accept", "text/html"}
	if r := vic.get(e.srv.URL+"/acme/shop", html...); r.status != 302 || r.location() != "/repositories/acme/shop" {
		t.Errorf("a reader: %d %q", r.status, r.location())
	}
	// A stranger, and a reader of another repository, are not told which repositories exist.
	for _, path := range []string{"/other/secret", "/acme/nothing"} {
		if r := vic.get(e.srv.URL+path, html...); r.status == 302 {
			t.Errorf("%s redirected for someone who may not read it", path)
		}
	}
	if r := newBrowser(t).get(e.srv.URL+"/acme/shop", html...); r.status == 302 {
		t.Error("an anonymous browser was redirected")
	}
	// The web UI's own two-segment paths are untouched.
	if r := vic.get(e.srv.URL+"/projects/pay-api", html...); r.status == 302 {
		t.Error("a UI path was redirected")
	}
}

func TestDoctorWarnsAboutAnOwnerThatCollidesWithAReservedPath(t *testing.T) {
	e, h := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	if w, err := h.Check(t.Context()); err != nil || len(w) != 0 {
		t.Fatalf("%v %v", w, err)
	}
	// Data that came in another way (an import): a repository under a reserved owner.
	if _, err := h.Forge.CreateRepository(t.Context(), forge.Repository{Connection: h.Connection.ID, Home: "api", Name: "x", Path: "api/x", Visibility: forge.Private}); err != nil {
		t.Fatal(err)
	}
	w, err := h.Check(t.Context())
	if err != nil || len(w) != 1 || !strings.Contains(w[0], "api/x") {
		t.Errorf("%v %v", w, err)
	}
}

// An unauthenticated clone of a private repository fails at once with an
// authentication error; git never falls back to a credential prompt (a
// desktop session's askpass helper would open a dialog).
func TestUnauthenticatedCloneOfAPrivateRepositoryFailsWithoutPrompting(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	start := time.Now()
	out, err := git(t, t.TempDir(), "clone", "-q", e.srv.URL+"/acme/shop.git", "c")
	if err == nil || time.Since(start) > testlib.Slow(20*time.Second) {
		t.Fatalf("clone of a private repository without credentials: %v after %v\n%s", err, time.Since(start), out)
	}
	if !strings.Contains(out, "401") && !strings.Contains(strings.ToLower(out), "authentication") && !strings.Contains(out, "terminal prompts disabled") && !strings.Contains(out, "could not read") {
		t.Errorf("not an authentication failure:\n%s", out)
	}
}
