package api

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/fakeidp"
)

type meView struct {
	User     string            `json:"user"`
	Groups   []string          `json:"groups"`
	AuthMode string            `json:"auth_mode"`
	Via      string            `json:"via"`
	Admin    bool              `json:"admin"`
	Runners  bool              `json:"runners"`
	Roles    map[string]string `json:"roles"`
}

func (e *authEnv) me(t *testing.T, b *browser, header ...string) (int, meView) {
	t.Helper()
	r := b.get(e.srv.URL+"/api/v1/me", header...)
	var m meView
	if r.status == http.StatusOK {
		r.json(t, &m)
	}
	return r.status, m
}

func TestSignIn(t *testing.T) {
	e := setupAuth(t, envOpts{})
	b := newBrowser(t)

	// Before signing in, the API refuses and info says only what sign-in needs.
	if code, _ := e.me(t, b); code != http.StatusUnauthorized {
		t.Fatalf("me before signing in: %d", code)
	}
	if r := b.get(e.srv.URL + "/api/v1/runs"); r.status != http.StatusUnauthorized {
		t.Fatalf("runs before signing in: %d", r.status)
	}
	var info map[string]any
	b.get(e.srv.URL+"/api/v1/info").json(t, &info)
	if len(info) != 2 || info["auth_mode"] != "oidc" || info["version"] == nil {
		t.Fatalf("info %v", info)
	}

	// The full flow, ending where the person wanted to go.
	e.idp.SignInAs("otto", "pay-ops", "unrelated")
	authURL := b.beginLogin(e.srv.URL, "/runs/run_x?step=deploy")
	if !strings.HasPrefix(authURL, e.idp.URL+"/authorize?") {
		t.Fatalf("login went to %s", authURL)
	}
	q := mustParse(t, authURL).Query()
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if len(q.Get(k)) < 43 {
			t.Errorf("%s %q is too short to be 256 random bits", k, q.Get(k))
		}
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("scope") != "openid profile email groups" {
		t.Errorf("authorize query %v", q)
	}
	r := b.get(b.authorize(authURL))
	if r.status != http.StatusFound || r.location() != "/runs/run_x?step=deploy" {
		t.Fatalf("callback: %d to %q: %s", r.status, r.location(), r.body)
	}
	line := setCookie(r, sessionCookie)
	for _, want := range []string{"Path=/", "HttpOnly", "SameSite=Lax", "Max-Age=86400"} {
		if !strings.Contains(line, want) {
			t.Errorf("session cookie %q lacks %s", line, want)
		}
	}
	if strings.Contains(line, "Secure") {
		t.Errorf("session cookie on plain HTTP is Secure: %q", line)
	}
	if raw := b.cookieValue(e.srv.URL, sessionCookie); len(raw) != 43 {
		t.Errorf("session id %q is not 256 bits", raw)
	}
	if !strings.Contains(setCookie(r, loginCookie), "Max-Age=0") {
		t.Errorf("the login cookie was not cleared: %q", setCookie(r, loginCookie))
	}
	code, m := e.me(t, b)
	if code != http.StatusOK || m.User != "otto" || !slices.Equal(m.Groups, []string{"pay-ops", "unrelated"}) ||
		m.Via != "session" || m.Admin || !m.Runners || m.Roles["pay-api"] != "operator" || m.Roles["web"] != "" {
		t.Fatalf("me: %d %+v", code, m)
	}
	last := e.auditEntries(t)
	if a := last[len(last)-1]; a.Action != "auth.sign_in" || a.User != "otto" || a.Outcome != "ok" || a.Source != "127.0.0.1" {
		t.Fatalf("audit %+v", a)
	}

	// Signing in again refreshes the groups.
	e.idp.SignInAs("otto", "pay-view")
	b2 := newBrowser(t)
	b2.login(e.srv.URL, "/")
	if _, m := e.me(t, b2); m.Roles["pay-api"] != "viewer" {
		t.Fatalf("after a second sign-in: %+v", m)
	}
	// ...for every session of that person, since groups belong to the person.
	if _, m := e.me(t, b); m.Roles["pay-api"] != "viewer" {
		t.Fatalf("the first session kept old groups: %+v", m)
	}
}

func mustParse(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Only paths on this server are accepted as somewhere to go after signing in.
func TestSignInRedirectsOnlyToThisServer(t *testing.T) {
	e := setupAuth(t, envOpts{})
	cases := map[string]string{
		"":                            "/",
		"/":                           "/",
		"/approvals":                  "/approvals",
		"/runs/r1?step=a%20b#x":       "/runs/r1?step=a%20b#x",
		"//evil.example.com/":         "/",
		"///evil.example.com/":        "/",
		"/\\evil.example.com":         "/",
		"\\\\evil.example.com":        "/",
		"https://evil.example.com/":   "/",
		"http:/evil.example.com":      "/",
		"javascript:alert(1)":         "/",
		"evil.example.com":            "/",
		"/%0d%0aSet-Cookie:x=y":       "/%0d%0aSet-Cookie:x=y", // escaped, so harmless
		"/a\r\nLocation: http://evil": "/",
		"/ok\t//evil":                 "/",
		"/auth/login?next=//evil":     "/",
	}
	for next, want := range cases {
		b := newBrowser(t)
		r := b.login(e.srv.URL, next)
		if r.status != http.StatusFound || r.location() != want {
			t.Errorf("next %q: %d to %q, want %q", next, r.status, r.location(), want)
		}
	}
}

func TestSignInRefusesForgedOrStaleState(t *testing.T) {
	e := setupAuth(t, envOpts{})
	wantRefused := func(name string, r response, code string) {
		t.Helper()
		if r.status != http.StatusFound || r.location() != "/signin?error="+code {
			t.Errorf("%s: %d to %q, want /signin?error=%s", name, r.status, r.location(), code)
		}
		if setCookie(r, sessionCookie) != "" {
			t.Errorf("%s: a session cookie was set", name)
		}
	}

	// A callback in a browser that never started a sign-in: someone trying
	// to sign a victim in as themselves.
	attacker := newBrowser(t)
	callback := attacker.authorize(attacker.beginLogin(e.srv.URL, "/"))
	wantRefused("someone else's callback", newBrowser(t).get(callback), "expired")

	// The state in the callback is not this browser's.
	b := newBrowser(t)
	cb := mustParse(t, b.authorize(b.beginLogin(e.srv.URL, "/")))
	q := cb.Query()
	q.Set("state", auth.RandomString(32))
	cb.RawQuery = q.Encode()
	wantRefused("a forged state", b.get(cb.String()), "state")

	// A login cookie that was altered.
	b = newBrowser(t)
	cbURL := b.authorize(b.beginLogin(e.srv.URL, "/"))
	u := mustParse(t, e.srv.URL)
	sealed := b.cookieValue(e.srv.URL+"/auth/", loginCookie)
	b.jar.SetCookies(u, []*http.Cookie{{Name: loginCookie, Value: sealed[:len(sealed)-4] + "AAAA", Path: "/auth/"}})
	wantRefused("an altered login cookie", b.get(cbURL), "expired")

	// A sign-in that took too long.
	b = newBrowser(t)
	cbURL = b.authorize(b.beginLogin(e.srv.URL, "/"))
	e.clk.Add(11 * time.Minute)
	wantRefused("a stale sign-in", b.get(cbURL), "expired")

	// A callback used twice.
	b = newBrowser(t)
	cbURL = b.authorize(b.beginLogin(e.srv.URL, "/"))
	if r := b.get(cbURL); r.status != http.StatusFound || r.location() != "/" {
		t.Fatalf("first use: %d %s", r.status, r.location())
	}
	wantRefused("a callback used twice", b.get(cbURL), "expired")

	// PKCE: a code stolen from one sign-in, brought back with another
	// sign-in's (valid) state, is refused by the provider because the
	// verifier is not the one the code was issued for.
	victim, thief := newBrowser(t), newBrowser(t)
	stolen := mustParse(t, victim.authorize(victim.beginLogin(e.srv.URL, "/"))).Query().Get("code")
	own := mustParse(t, thief.authorize(thief.beginLogin(e.srv.URL, "/")))
	q = own.Query()
	q.Set("code", stolen)
	own.RawQuery = q.Encode()
	wantRefused("a stolen code", thief.get(own.String()), "failed")
	if refused := e.idp.Refused(); !slices.Contains(refused, "PKCE verification failed") {
		t.Errorf("the provider did not see a PKCE failure: %v", refused)
	}

	// The person refuses at the provider.
	e.idp.Fail(fakeidp.Deny)
	b = newBrowser(t)
	wantRefused("refused at the provider", b.login(e.srv.URL, "/"), "provider")
	e.idp.Fail(fakeidp.None)

	// Every failure is in the audit trail, with no user.
	failed := 0
	for _, a := range e.auditEntries(t) {
		if a.Action == "auth.sign_in" && a.Outcome == "failed" {
			failed++
			if a.Detail == "" || a.User != "" {
				t.Errorf("failed sign-in recorded as %+v", a)
			}
		}
	}
	if failed != 7 {
		t.Errorf("%d failed sign-ins recorded, want 7", failed)
	}
}

func TestSignInChecksTheIDToken(t *testing.T) {
	e := setupAuth(t, envOpts{})
	for f, want := range map[fakeidp.Failure]string{
		fakeidp.WrongNonce:    "nonce does not match",
		fakeidp.WrongAudience: "audience",
		fakeidp.WrongIssuer:   "issued by a different provider",
		fakeidp.Expired:       "token is expired",
		fakeidp.BadSignature:  "failed to verify signature",
	} {
		e.idp.Fail(f)
		b := newBrowser(t)
		r := b.login(e.srv.URL, "/")
		if r.location() != "/signin?error=failed" || setCookie(r, sessionCookie) != "" {
			t.Errorf("%s: %d to %q, cookie %q", f, r.status, r.location(), setCookie(r, sessionCookie))
		}
		if code, _ := e.me(t, b); code != http.StatusUnauthorized {
			t.Errorf("%s: signed in anyway", f)
		}
		all := e.auditEntries(t)
		if a := all[len(all)-1]; a.Outcome != "failed" || !strings.Contains(a.Detail, want) {
			t.Errorf("%s: audit %+v, want detail containing %q", f, a, want)
		}
	}
}

// A provider that sends groups only from its userinfo endpoint still works.
func TestSignInReadsGroupsFromUserinfo(t *testing.T) {
	e := setupAuth(t, envOpts{})
	e.idp.Fail(fakeidp.NoGroups)
	e.idp.SignInAs("vic", "pay-view")
	b := newBrowser(t)
	b.login(e.srv.URL, "/")
	if code, m := e.me(t, b); code != http.StatusOK || !slices.Equal(m.Groups, []string{"pay-view"}) {
		t.Fatalf("%d %+v", code, m)
	}
}

func TestSignOut(t *testing.T) {
	e := setupAuth(t, envOpts{})
	b := e.signIn(t, "vic")
	raw := b.cookieValue(e.srv.URL, sessionCookie)

	// Another site cannot sign people out.
	if r := b.do(http.MethodPost, e.srv.URL+"/auth/logout", "", csrfHeader, ""); r.status != http.StatusForbidden {
		t.Fatalf("logout without the CSRF header: %d", r.status)
	}
	if r := b.do(http.MethodPost, e.srv.URL+"/auth/logout", "", "Origin", "https://evil.example.com"); r.status != http.StatusForbidden {
		t.Fatalf("logout from another origin: %d", r.status)
	}
	if code, _ := e.me(t, b); code != http.StatusOK {
		t.Fatal("signed out by a refused request")
	}
	r := b.do(http.MethodPost, e.srv.URL+"/auth/logout", "", "Origin", e.srv.URL)
	if r.status != http.StatusNoContent || !strings.Contains(setCookie(r, sessionCookie), "Max-Age=0") {
		t.Fatalf("logout: %d %q", r.status, setCookie(r, sessionCookie))
	}
	// The session is gone on the server, not only from the browser.
	replay := newBrowser(t)
	replay.jar.SetCookies(mustParse(t, e.srv.URL), []*http.Cookie{{Name: sessionCookie, Value: raw, Path: "/"}})
	if code, _ := e.me(t, replay); code != http.StatusUnauthorized {
		t.Fatalf("the old session still works: %d", code)
	}
	all := e.auditEntries(t)
	if a := all[len(all)-1]; a.Action != "auth.sign_out" || a.User != "vic" || a.Outcome != "ok" {
		t.Fatalf("audit %+v", a)
	}
}

func TestSessionsExpire(t *testing.T) {
	e := setupAuth(t, envOpts{})

	// Idle: each use moves the deadline on...
	b := e.signIn(t, "vic")
	for i := 0; i < 3; i++ {
		e.clk.Add(7*time.Hour + 59*time.Minute)
		if code, _ := e.me(t, b); code != http.StatusOK {
			t.Fatalf("after %d idle periods: %d", i+1, code)
		}
	}
	// ...but the absolute lifetime does not move: 23h57m have passed.
	e.clk.Add(4 * time.Minute)
	if code, _ := e.me(t, b); code != http.StatusUnauthorized {
		t.Fatalf("past the absolute lifetime: %d", code)
	}

	// Idle for too long.
	b = e.signIn(t, "vic")
	e.clk.Add(8*time.Hour + time.Second)
	if code, _ := e.me(t, b); code != http.StatusUnauthorized {
		t.Fatalf("past the idle timeout: %d", code)
	}

	// What the UI does by itself (polling) does not count as activity.
	b = e.signIn(t, "vic")
	e.clk.Add(4 * time.Hour)
	if code, _ := e.me(t, b, backgroundHeader, "1"); code != http.StatusOK {
		t.Fatalf("background request: %d", code)
	}
	e.clk.Add(4*time.Hour + time.Second)
	if code, _ := e.me(t, b); code != http.StatusUnauthorized {
		t.Fatalf("background requests kept the session alive: %d", code)
	}
}

func TestCSRF(t *testing.T) {
	e := setupAuth(t, envOpts{})
	b := e.signIn(t, "otto")
	start := e.srv.URL + "/api/v1/projects/pay-api/runs"
	for _, c := range []struct {
		name   string
		header []string
		want   int
	}{
		{"no CSRF header", []string{csrfHeader, ""}, http.StatusForbidden},
		{"a wrong CSRF header", []string{csrfHeader, "yes"}, http.StatusForbidden},
		{"another origin", []string{"Origin", "https://evil.example.com"}, http.StatusForbidden},
		{"a look-alike origin", []string{"Origin", e.srv.URL + ".evil.example.com"}, http.StatusForbidden},
		{"a null origin", []string{"Origin", "null"}, http.StatusForbidden},
		{"this origin", []string{"Origin", e.srv.URL}, http.StatusCreated},
		{"no origin", nil, http.StatusCreated},
	} {
		if r := b.do(http.MethodPost, start, `{"ref": "main"}`, c.header...); r.status != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, r.status, r.body, c.want)
		}
	}
	// A token is not sent by browsers on their own, so it needs no header.
	_, tok := b.newToken(e.srv.URL, "ci")
	if r := newBrowser(t).do(http.MethodPost, start, `{"ref": "main"}`, append(bearer(tok), csrfHeader, "")...); r.status != http.StatusCreated {
		t.Errorf("with a token: %d %s", r.status, r.body)
	}
	// Refused attempts are audited.
	denied := 0
	for _, a := range e.auditEntries(t) {
		if a.Action == "run.start" && a.Outcome == "denied" && a.User == "otto" {
			denied++
		}
	}
	if denied != 5 {
		t.Errorf("%d denied starts audited, want 5", denied)
	}
}

// Without sign-in, requests that change something still need the header,
// so a web page cannot drive a server on localhost.
func TestCSRFWithoutSignIn(t *testing.T) {
	e := setup(t, time.Second)
	if code, _ := post(t, e.srv.URL+"/api/v1/runs/run_x/cancel", ""); code != http.StatusNotFound {
		t.Fatalf("with the header: %d", code)
	}
	resp, err := http.Post(e.srv.URL+"/api/v1/runs/run_x/cancel", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without the header: %d", resp.StatusCode)
	}
}

func TestCookieFlags(t *testing.T) {
	cases := []struct {
		name          string
		trustProxy    bool
		forwarded     string
		secureCookies bool
		wantSecure    bool
	}{
		{"plain HTTP", false, "", false, false},
		{"a forwarded proto nobody trusts", false, "https", false, false},
		{"a trusted proxy", true, "https", false, true},
		{"a trusted proxy on plain HTTP", true, "http", false, false},
		{"an https redirect URL", false, "", true, true},
	}
	for _, c := range cases {
		e := setupAuth(t, envOpts{trustProxy: c.trustProxy})
		e.api.Auth.SecureCookies = c.secureCookies
		b := newBrowser(t)
		hdr := []string{"X-Forwarded-Proto", c.forwarded}
		r := b.get(e.srv.URL+"/auth/login", hdr...)
		cb := b.authorize(r.location())
		// The jar would not send a Secure cookie over plain HTTP; send it by hand.
		req, _ := http.NewRequest(http.MethodGet, cb, nil)
		req.Header.Set("X-Forwarded-Proto", c.forwarded)
		for _, line := range r.header.Values("Set-Cookie") {
			if c, err := http.ParseSetCookie(line); err == nil {
				req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
			}
		}
		resp, err := b.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, line := range append(r.header.Values("Set-Cookie"), resp.Header.Values("Set-Cookie")...) {
			if strings.Contains(line, "Secure") != c.wantSecure || !strings.Contains(line, "HttpOnly") || !strings.Contains(line, "SameSite=Lax") {
				t.Errorf("%s: %q", c.name, line)
			}
		}
		if setCookie(response{header: resp.Header}, sessionCookie) == "" {
			t.Errorf("%s: no session: %s", c.name, resp.Header.Get("Location"))
		}
	}
}

// Behind a trusted proxy the audit trail records the address the proxy saw,
// not one the client made up.
func TestAuditSourceBehindProxy(t *testing.T) {
	e := setupAuth(t, envOpts{trustProxy: true})
	e.idp.SignInAs("vic", "pay-view")
	b := newBrowser(t)
	b.get(b.authorize(b.beginLogin(e.srv.URL, "/")), "X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	all := e.auditEntries(t)
	if a := all[len(all)-1]; a.Action != "auth.sign_in" || a.Source != "203.0.113.9" {
		t.Fatalf("audit %+v", a)
	}
	e2 := setupAuth(t, envOpts{})
	b = newBrowser(t)
	b.get(b.authorize(b.beginLogin(e2.srv.URL, "/")), "X-Forwarded-For", "203.0.113.9")
	all = e2.auditEntries(t)
	if a := all[len(all)-1]; a.Source != "127.0.0.1" {
		t.Fatalf("an untrusted X-Forwarded-For was believed: %+v", a)
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := setupAuth(t, envOpts{})
	r := newBrowser(t).get(e.srv.URL + "/")
	if r.header.Get("X-Frame-Options") != "DENY" || r.header.Get("Content-Security-Policy") != "frame-ancestors 'none'" {
		t.Fatalf("headers %v", r.header)
	}
}
