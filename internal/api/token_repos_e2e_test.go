package api

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

// repoToken makes a token for ada with a limit on repositories.
func repoToken(t *testing.T, e *authEnv, body string) (id, tok string) {
	t.Helper()
	id, tok, r := e.signIn(t, "ada").makeToken(e.srv.URL, body)
	if r.status != http.StatusCreated {
		t.Fatalf("making the token %s: %d %s", body, r.status, r.body)
	}
	return id, tok
}

func gitStatus(e *authEnv, tok, path, svc string) int {
	code, _ := gitProbe(e, tok, path, svc)
	return code
}

// gitProbe is the status of a Git advertisement request, and whether the
// answer really was a Git advertisement (not the web UI's page).
func gitProbe(e *authEnv, tok, path, svc string) (int, bool) {
	req, _ := http.NewRequest("GET", e.srv.URL+path+"/info/refs?service="+svc, nil)
	req.SetBasicAuth("x", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1, false
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, strings.Contains(string(buf[:n]), "# service=")
}

// A token limited to repositories reaches those and nothing else: not another
// repository, not the Flight APIs, not the administration, not the registry,
// and it cannot make a token.
func TestRepoLimitedTokenReachesOnlyItsRepositories(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	for _, p := range []string{"acme/shop", "acme/other", "acme-other/x", "other/secret"} {
		hostedRepo(t, e, p)
	}
	_, rd := repoToken(t, e, `{"name":"mirror","repositories":["acme/shop"],"repo_access":"read"}`)
	_, wr := repoToken(t, e, `{"name":"pusher","repositories":["acme/shop","acme/other"],"repo_access":"write"}`)
	cli := newBrowser(t)

	// Git: the listed repository only; read cannot push.
	if got := gitStatus(e, rd, "/acme/shop", "git-upload-pack"); got != 200 {
		t.Errorf("reading its repository: %d", got)
	}
	for _, p := range []string{"/other/secret", "/acme/other", "/acme-other/x", "/acme/nothere"} {
		if got := gitStatus(e, rd, p, "git-upload-pack"); got != 404 {
			t.Errorf("reading %s with a token for acme/shop: %d, want 404", p, got)
		}
	}
	if got := gitStatus(e, rd, "/acme/shop", "git-receive-pack"); got != 403 {
		t.Errorf("a read token pushing: %d, want 403", got)
	}
	if got := gitStatus(e, wr, "/acme/other", "git-receive-pack"); got != 200 {
		t.Errorf("a write token pushing to a listed repository: %d", got)
	}
	if got := gitStatus(e, wr, "/other/secret", "git-receive-pack"); got != 404 {
		t.Errorf("a write token pushing elsewhere: %d", got)
	}
	// A real push and clone.
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/shop", wr), "HEAD:refs/heads/main")
	c := t.TempDir()
	mustGit(t, c, "clone", "-q", withToken(e.srv.URL, "/git/acme/shop.git", rd), "c")
	if out, err := git(t, src, "push", "-q", withToken(e.srv.URL, "/acme/shop", rd), "HEAD:refs/heads/x"); err == nil {
		t.Errorf("a read token pushed:\n%s", out)
	}

	// The repository API: listing shows only what it covers.
	var list forge.Page[forge.Repository]
	cli.get(e.srv.URL+"/api/v1/repositories", bearer(wr)...).json(t, &list)
	if len(list.Items) != 2 {
		t.Errorf("the write token lists %d repositories, want 2: %+v", len(list.Items), list.Items)
	}
	for _, c := range []struct {
		method, path, body string
		tok                string
		want               int
	}{
		{"GET", "/api/v1/repositories/acme/shop", "", rd, 200},
		{"GET", "/api/v1/repositories/other/secret", "", rd, 404},
		{"PATCH", "/api/v1/repositories/acme/shop", `{"archived":true}`, wr, 403}, // admin of a repository: never
		{"DELETE", "/api/v1/repositories/acme/shop", "", wr, 403},
		{"POST", "/api/v1/repositories", `{"owner":"acme","name":"new"}`, wr, 403},
		{"GET", "/api/v1/projects", "", rd, 403},
		{"GET", "/api/v1/runs", "", rd, 403},
		{"GET", "/api/v1/audit", "", rd, 403},
		{"GET", "/api/v1/tokens", "", rd, 403},
		{"POST", "/api/v1/tokens", `{"name":"child"}`, rd, 403},
		{"GET", "/api/v1/admin/tokens", "", rd, 403},
		{"GET", "/api/v1/service-accounts", "", rd, 403},
		{"POST", "/api/v1/projects/acme%2Fshop/runs", `{"ref":"main"}`, wr, 403},
		{"GET", "/api/v1/projects/acme%2Fshop", "", wr, 403},
		{"GET", "/api/v1/stream", "", rd, 403},
		{"GET", "/api/v1/users", "", rd, 403},
		{"GET", "/v2/", "", rd, -1},
	} {
		got := cli.statusOf(c.method, e.srv.URL+c.path, c.body, bearer(c.tok)...)
		if c.want == -1 {
			if got == 200 {
				t.Errorf("%s %s with a repository token: %d", c.method, c.path, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s %s with a repository token: %d, want %d", c.method, c.path, got, c.want)
		}
	}
	var me struct {
		Admin bool              `json:"admin"`
		Roles map[string]string `json:"roles"`
	}
	cli.get(e.srv.URL+"/api/v1/me", bearer(wr)...).json(t, &me)
	if me.Admin || len(me.Roles) != 0 {
		t.Errorf("me with a repository token: %+v", me)
	}
}

// acme/* is the repositories of owner acme, not of acme-other; the name is
// compared whole, and nothing odd in a path widens it.
func TestRepoWildcardAndPathTricks(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	for _, p := range []string{"acme/shop", "acme-other/x", "other/secret"} {
		hostedRepo(t, e, p)
	}
	_, tok := repoToken(t, e, `{"name":"owner","repositories":["acme/*"],"repo_access":"read"}`)
	for p, want := range map[string]int{
		"/acme/shop": 200, "/git/acme/shop.git": 200, "/acme/shop.git": 200,
		"/acme-other/x": 404, "/other/secret": 404, "/ACME/shop": 404, "/Acme/shop.git": 404,
		"/acme/shop/../../other/secret": 404, "/acme/%2e%2e/other/secret": 404, "/acme/shop%2f..%2f..%2fother/secret": 404,
		"/acme/": 404,
	} {
		got, adv := gitProbe(e, tok, p, "git-upload-pack")
		if (want == 200) != adv || (want == 200 && got != 200) {
			t.Errorf("%s: status %d, a Git advertisement %v; want one: %v", p, got, adv, want == 200)
		}
	}
	// Unit: the matcher is exact on both halves.
	ent := []engine.TokenRepo{{Pattern: "acme/*"}, {Pattern: "ops/tool"}}
	for p, want := range map[string]bool{"acme/shop": true, "acme/": false, "acme": false, "acme-other/x": false, "acme/a/b": false,
		"ops/tool": true, "ops/tool2": false, "ops/tools": false, "xops/tool": false, "": false, "/shop": false, "ACME/shop": false} {
		if coversRepo(ent, p) != want {
			t.Errorf("coversRepo(%q) = %v", p, !want)
		}
	}
	// A limit on repositories and one on Flights do not mix; a malformed
	// entry, a missing repository and a missing level are refused.
	ada := e.signIn(t, "ada")
	for _, body := range []string{
		`{"name":"a","repositories":["acme/*"],"repo_access":"read","projects":["pay-*"]}`,
		`{"name":"a","repositories":["acme"],"repo_access":"read"}`,
		`{"name":"a","repositories":["acme/sh*"],"repo_access":"read"}`,
		`{"name":"a","repositories":["*/shop"],"repo_access":"read"}`,
		`{"name":"a","repositories":["acme/missing"],"repo_access":"read"}`,
		`{"name":"a","repositories":["acme/shop"]}`,
		`{"name":"a","repositories":["acme/shop"],"repo_access":"admin"}`,
		`{"name":"a","repo_access":"read"}`,
		`{"name":"a","repositories":["acme/shop/../x"],"repo_access":"read"}`,
	} {
		if _, _, r := ada.makeToken(e.srv.URL, body); r.status != 400 {
			t.Errorf("%s: %d %s", body, r.status, r.body)
		}
	}
}

// A repository deleted and made again under a token's name is not reachable
// with it; a renamed one still is.
func TestRepoTokenFollowsRenameAndForgetsDelete(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	_, tok := repoToken(t, e, `{"name":"shop","repositories":["acme/shop"],"repo_access":"read"}`)
	ada := e.signIn(t, "ada")
	if r := ada.do("PATCH", e.srv.URL+"/api/v1/repositories/acme/shop", `{"name":"store"}`); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	if got := gitStatus(e, tok, "/acme/store", "git-upload-pack"); got != 200 {
		t.Errorf("the token after the rename: %d", got)
	}
	if got := gitStatus(e, tok, "/acme/shop", "git-upload-pack"); got != 200 {
		t.Errorf("the old name after the rename (it keeps working until another repository takes it): %d", got)
	}
	// Another repository made under the old name is not covered.
	hostedRepo(t, e, "acme/shop")
	if got := gitStatus(e, tok, "/acme/shop", "git-upload-pack"); got != 404 {
		t.Errorf("a new repository under the renamed one's old name: %d", got)
	}
	if r := ada.do("DELETE", e.srv.URL+"/api/v1/repositories/acme/store", ""); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	hostedRepo(t, e, "acme/store")
	if got := gitStatus(e, tok, "/acme/store", "git-upload-pack"); got != 404 {
		t.Errorf("a repository made again under the deleted one's name: %d", got)
	}
	var list []engine.APIToken
	ada.get(e.srv.URL+"/api/v1/tokens").json(t, &list)
	if len(list) != 1 || list[0].RepoAccess != "read" || len(list[0].Repositories) != 0 {
		t.Errorf("the token should stay limited to repositories, with none left: %+v", list)
	}
}

// Tokens made before the limits existed have none, and work as before.
func TestTokenWithoutNewLimitsIsUnchanged(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	tok := e.tokenFor(t, "ada", "plain")
	if got := gitStatus(e, tok, "/acme/shop", "git-receive-pack"); got != 200 {
		t.Errorf("a plain token: %d", got)
	}
	if got := newBrowser(t).statusOf("GET", e.srv.URL+"/api/v1/audit", "", bearer(tok)...); got != 200 {
		t.Errorf("a plain token on the audit trail: %d", got)
	}
}

func fakeReq(remote string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// Forwarding headers are believed only from a configured proxy.
func TestClientAddressBehindAProxy(t *testing.T) {
	pfx := func(c ...string) []netip.Prefix {
		var out []netip.Prefix
		for _, x := range c {
			out = append(out, netip.MustParsePrefix(x))
		}
		return out
	}
	type tc struct {
		name string
		s    *Server
		r    *http.Request
		want string
	}
	for _, c := range []tc{
		{"no proxy trusted: the header is ignored", &Server{}, fakeReq("198.51.100.4:5000", map[string]string{"X-Forwarded-For": "203.0.113.9"}), "198.51.100.4"},
		{"legacy trust_proxy: the last entry", &Server{TrustProxy: true}, fakeReq("198.51.100.4:5000", map[string]string{"X-Forwarded-For": "1.1.1.1, 203.0.113.9"}), "203.0.113.9"},
		{"a proxy in range: the last entry", &Server{TrustedProxies: pfx("10.0.0.0/8")}, fakeReq("10.1.2.3:5000", map[string]string{"X-Forwarded-For": "6.6.6.6, 203.0.113.9"}), "203.0.113.9"},
		{"a peer outside the ranges cannot forge it", &Server{TrustedProxies: pfx("10.0.0.0/8")}, fakeReq("198.51.100.4:5000", map[string]string{"X-Forwarded-For": "203.0.113.9"}), "198.51.100.4"},
		{"trust_proxy does not widen the ranges", &Server{TrustProxy: true, TrustedProxies: pfx("10.0.0.0/8")}, fakeReq("198.51.100.4:5000", map[string]string{"X-Forwarded-For": "203.0.113.9"}), "198.51.100.4"},
		{"two proxies: skip our own", &Server{TrustedProxies: pfx("10.0.0.0/8")}, fakeReq("10.0.0.1:1", map[string]string{"X-Forwarded-For": "203.0.113.9, 10.0.0.7"}), "203.0.113.9"},
		{"a client-set entry behind our proxy is not believed over the proxy's", &Server{TrustedProxies: pfx("10.0.0.0/8")}, fakeReq("10.0.0.1:1", map[string]string{"X-Forwarded-For": "9.9.9.9, 203.0.113.9"}), "203.0.113.9"},
		{"a malformed entry falls back to the peer", &Server{TrustedProxies: pfx("10.0.0.0/8")}, fakeReq("10.0.0.1:1", map[string]string{"X-Forwarded-For": "not-an-address"}), "10.0.0.1"},
		{"the edge's single-address header", &Server{TrustedProxies: pfx("10.0.0.0/8"), ClientAddrHeader: "X-Real-IP"}, fakeReq("10.0.0.1:1", map[string]string{"X-Real-IP": "203.0.113.9", "X-Forwarded-For": "6.6.6.6"}), "203.0.113.9"},
		{"the single-address header from an untrusted peer", &Server{TrustedProxies: pfx("10.0.0.0/8"), ClientAddrHeader: "X-Real-IP"}, fakeReq("198.51.100.4:1", map[string]string{"X-Real-IP": "203.0.113.9"}), "198.51.100.4"},
		{"a single-address header that is a list is refused", &Server{TrustedProxies: pfx("10.0.0.0/8"), ClientAddrHeader: "X-Real-IP"}, fakeReq("10.0.0.1:1", map[string]string{"X-Real-IP": "6.6.6.6, 203.0.113.9"}), "10.0.0.1"},
		{"an IPv4-mapped address is the IPv4 address", &Server{TrustedProxies: pfx("10.0.0.0/8")}, fakeReq("10.0.0.1:1", map[string]string{"X-Forwarded-For": "::ffff:203.0.113.9"}), "203.0.113.9"},
	} {
		if got := c.s.clientAddr(c.r); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if (&Server{TrustedProxies: pfx("10.0.0.0/8")}).isHTTPS(fakeReq("198.51.100.4:1", map[string]string{"X-Forwarded-Proto": "https"})) {
		t.Error("X-Forwarded-Proto was believed from an untrusted peer")
	}
}

// A token limited to source ranges is refused from elsewhere, with a plain
// message, and the refusal is audited by token; a forged forwarding header
// from a peer that is not a trusted proxy does not help.
func TestTokenSourceRanges(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	ada := e.signIn(t, "ada")
	cli := newBrowser(t)
	for _, body := range []string{`{"name":"a","source_ranges":["nonsense"]}`, `{"name":"a","source_ranges":["0.0.0.0/0"]}`, `{"name":"a","source_ranges":["10.0.0.0/33"]}`} {
		if _, _, r := ada.makeToken(e.srv.URL, body); r.status != 400 {
			t.Errorf("%s: %d", body, r.status)
		}
	}
	_, elsewhere, r := ada.makeToken(e.srv.URL, `{"name":"far","source_ranges":["203.0.113.0/24"]}`)
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	idHere, here, r := ada.makeToken(e.srv.URL, `{"name":"near","source_ranges":["127.0.0.1","::1/128"]}`)
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	got := cli.do("GET", e.srv.URL+"/api/v1/me", "", bearer(elsewhere)...)
	if got.status != 403 || !strings.Contains(string(got.body), "may only be used from 203.0.113.0/24; this request came from 127.0.0.1") {
		t.Errorf("a token used from elsewhere: %d %s", got.status, got.body)
	}
	// A forged header from a peer nobody trusts changes nothing, with the legacy
	// switch off and with ranges that do not include the peer.
	if s := cli.statusOf("GET", e.srv.URL+"/api/v1/me", "", append(bearer(elsewhere), "X-Forwarded-For", "203.0.113.9")...); s != 403 {
		t.Errorf("a forged X-Forwarded-For: %d", s)
	}
	e.api.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	if s := cli.statusOf("GET", e.srv.URL+"/api/v1/me", "", append(bearer(elsewhere), "X-Forwarded-For", "203.0.113.9")...); s != 403 {
		t.Errorf("a forged X-Forwarded-For with proxy ranges that exclude the peer: %d", s)
	}
	// From a trusted proxy the header is believed.
	e.api.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	if s := cli.statusOf("GET", e.srv.URL+"/api/v1/me", "", append(bearer(elsewhere), "X-Forwarded-For", "203.0.113.9")...); s != 200 {
		t.Errorf("a header from a trusted proxy: %d", s)
	}
	if s := cli.statusOf("GET", e.srv.URL+"/api/v1/me", "", append(bearer(here), "X-Forwarded-For", "203.0.113.9")...); s != 403 {
		t.Errorf("the near token from the far address: %d", s)
	}
	e.api.TrustedProxies = nil
	if s := cli.statusOf("GET", e.srv.URL+"/api/v1/me", "", bearer(here)...); s != 200 {
		t.Errorf("the near token from the near address: %d", s)
	}
	// Git too, and the refusal names the token in the audit trail.
	if s := gitStatus(e, elsewhere, "/acme/shop", "git-upload-pack"); s != 403 {
		t.Errorf("git with a token from elsewhere: %d", s)
	}
	var denied bool
	for _, a := range e.auditEntries(t) {
		if a.Action == "token.use" && a.Outcome == "denied" && a.TokenName == "far" && a.TokenID != "" && strings.Contains(a.Detail, "may only be used from") {
			denied = true
		}
	}
	if !denied {
		t.Errorf("the refusal is not audited by token: %+v", e.auditEntries(t))
	}
	_ = idHere
}

// The audit line of a push, and of an API change, names the token (id and
// name), never the secret.
func TestAuditNamesTheToken(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	id, tok := repoToken(t, e, `{"name":"pusher","repositories":["acme/shop"],"repo_access":"write"}`)
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", withToken(e.srv.URL, "/acme/shop", tok), "HEAD:refs/heads/main")
	adaTok := e.tokenFor(t, "ada", "api-key")
	if r := newBrowser(t).do("POST", e.srv.URL+"/api/v1/repositories", `{"owner":"acme","name":"two"}`, bearer(adaTok)...); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	var push, change bool
	for _, a := range e.auditEntries(t) {
		if strings.Contains(a.Detail+a.Target+a.TokenName, tok) {
			t.Errorf("the secret is in the audit trail: %+v", a)
		}
		push = push || (a.Action == "git.push" && a.Outcome == "ok" && a.TokenID == id && a.TokenName == "pusher")
		change = change || (a.Action == "repository.create" && a.TokenName == "api-key" && strings.HasPrefix(a.TokenID, "tok_"))
	}
	if !push || !change {
		t.Errorf("push named %v, change named %v: %+v", push, change, e.auditEntries(t))
	}
	_ = filepath.Join
}

func gitStatusBearer(e *authEnv, tok, path, svc string) int {
	req, _ := http.NewRequest("GET", e.srv.URL+path+"/info/refs?service="+svc, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return -1
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The two kinds of limit combine by the most restrictive: a viewer role cap on a
// write token makes it read-only; and no repository token is anything but a
// reader or operator of its repositories, whatever the checks are asked.
func TestRepoTokenCombinesWithRoleAndAsksNothingElse(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	_, tok := repoToken(t, e, `{"name":"capped","role":"viewer","repositories":["acme/shop"],"repo_access":"write"}`)
	if got := gitStatus(e, tok, "/acme/shop", "git-upload-pack"); got != 200 {
		t.Errorf("reading: %d", got)
	}
	if got := gitStatus(e, tok, "/acme/shop", "git-receive-pack"); got != 403 {
		t.Errorf("a write token with a viewer cap pushing: %d, want 403", got)
	}
	id := &identity{User: "ada", Groups: []string{"admins"}, Via: viaToken, repoAccess: "write", repos: []engine.TokenRepo{{Pattern: "acme/*"}}}
	for _, c := range []struct {
		a       auth.Action
		project string
		want    bool
	}{
		{auth.Read, "acme/shop", true}, {auth.Operate, "acme/shop", true}, {auth.AdministerProject, "acme/shop", false},
		{auth.Read, "acme-other/x", false}, {auth.Read, "acme", false}, {auth.Read, "pay-api", false}, {auth.Read, "", false},
		{auth.ReadAny, "", false}, {auth.Administer, "", false}, {auth.RunDraft, "pay-api", false}, {auth.RunDraft, "acme/shop", false},
	} {
		if got := e.api.authorize(id, c.a, c.project); got != c.want {
			t.Errorf("authorize(%v, %q) = %v, want %v", c.a, c.project, got, c.want)
		}
	}
}

// A repository removed without the API (so no token was rewritten) and made again under a listed name is
// not reachable by ID-bound tokens on any route class: read, write, list, get.
func TestRepoTokenIsBoundByIDOnEveryRoute(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	old := hostedRepo(t, e, "acme/shop")
	_, tok := repoToken(t, e, `{"name":"bound","repositories":["acme/shop"],"repo_access":"write"}`)
	cli, base := newBrowser(t), e.srv.URL
	probe := func(want int) {
		t.Helper()
		for _, c := range []struct{ m, p, b string }{
			{"GET", "/api/v1/repositories/acme/shop", ""}, {"GET", "/api/v1/repositories/acme/shop/branches", ""},
			{"PUT", "/api/v1/repositories/acme/shop/files", `{"path":"a.txt","content":"eA==","message":"m","branch":"main"}`},
			{"POST", "/api/v1/repositories/acme/shop/graph/refresh", ""},
		} {
			r := cli.do(c.m, base+c.p, c.b, bearer(tok)...)
			gone := r.status == 404 && strings.Contains(string(r.body), "no such repository")
			if (want == 200) == gone {
				t.Errorf("%s %s: %d %s (the repository is reachable: %v)", c.m, c.p, r.status, r.body, want == 200)
			}
		}
		if got := gitStatus(e, tok, "/acme/shop", "git-upload-pack"); (got == 200) != (want == 200) {
			t.Errorf("git: %d", got)
		}
		var list forge.Page[forge.Repository]
		cli.get(base+"/api/v1/repositories", bearer(tok)...).json(t, &list)
		if (len(list.Items) == 1) != (want == 200) {
			t.Errorf("the listing has %d repositories", len(list.Items))
		}
	}
	probe(200)
	// Removed behind the API's back, then made again: a different repository with the same name.
	if err := e.api.Forge.Provider.Repositories().Delete(t.Context(), "acme/shop"); err != nil {
		t.Fatal(err)
	}
	fresh := hostedRepo(t, e, "acme/shop")
	if fresh.ID == old.ID {
		t.Fatal("the repository kept its ID")
	}
	probe(404)
}
