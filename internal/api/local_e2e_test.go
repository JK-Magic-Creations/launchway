package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/localauth"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

type localEnv struct {
	srv  *httptest.Server
	api  *Server
	eng  *engine.Engine
	clk  *testClock
	code string
}

const goodPassword = "purple walrus drinks coffee"

func setupLocal(t *testing.T, mutate func(*LocalAuth)) *localEnv {
	t.Helper()
	clk := &testClock{t: testClockStart}
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := auth.NewPolicy([]auth.Binding{{Group: auth.LocalGroup(auth.Admin), Role: auth.Admin},
		{Group: auth.LocalGroup(auth.Operator), Role: auth.Operator}, {Group: auth.LocalGroup(auth.Viewer), Role: auth.Viewer}})
	if err != nil {
		t.Fatal(err)
	}
	la := &LocalAuth{Required: true, SecondFactor: "admins"}
	if mutate != nil {
		mutate(la)
	}
	s := &Server{Engine: eng, RunnerToken: "runner-secret", Log: quiet(), Now: clk.Now,
		Auth: &Auth{Policy: pol, IdleTimeout: 8 * time.Hour, Lifetime: 24 * time.Hour, Local: la}}
	s.Settings = testSettings(t, eng)
	s.Variables = testVariables(eng)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	code, err := s.EnsureSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	return &localEnv{srv: srv, api: s, eng: eng, clk: clk, code: code}
}

func (e *localEnv) post(b *browser, path string, body any) (int, map[string]any) {
	b.t.Helper()
	raw, _ := json.Marshal(body)
	r := b.do(http.MethodPost, e.srv.URL+path, string(raw))
	var out map[string]any
	_ = json.Unmarshal(r.body, &out)
	return r.status, out
}

func (e *localEnv) totp(t *testing.T, secret string) string {
	t.Helper()
	c, err := localauth.TOTPCode(secret, e.clk.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// setUpAdmin runs the first-run flow in a browser and returns the TOTP secret and recovery codes.
func (e *localEnv) setUpAdmin(t *testing.T, b *browser, name string) (string, []any) {
	t.Helper()
	st, begin := e.post(b, "/api/v1/setup/begin", map[string]string{"code": e.code, "username": name, "display_name": "Ada", "password": goodPassword})
	if st != 200 {
		t.Fatalf("setup begin: %d %v", st, begin)
	}
	secret := begin["totp_secret"].(string)
	st, done := e.post(b, "/api/v1/setup/complete", map[string]string{"pending": begin["pending"].(string), "code": e.totp(t, secret)})
	if st != 200 || done["signed_in"] != true {
		t.Fatalf("setup complete: %d %v", st, done)
	}
	return secret, done["recovery_codes"].([]any)
}

func TestFirstAdministerWithSetupCode(t *testing.T) {
	e := setupLocal(t, nil)
	b := newBrowser(t)
	if e.code == "" || strings.Count(e.code, "-") != 4 {
		t.Fatalf("the server made no setup code: %q", e.code)
	}
	// Until setup completes, nothing else in the API works.
	for _, p := range []string{"/api/v1/me", "/api/v1/runs", "/api/v1/users"} {
		r := b.get(e.srv.URL + p)
		var body map[string]string
		r.json(t, &body)
		if r.status != http.StatusServiceUnavailable || body["code"] != "setup_required" || body["next"] == "" {
			t.Errorf("%s before setup: %d %v", p, r.status, body)
		}
	}
	var info map[string]any
	b.get(e.srv.URL + "/api/v1/info").json(t, &info)
	if info["auth_mode"] != "local" || info["setup_required"] != true {
		t.Fatalf("info %v", info)
	}
	// A wrong code, and a weak password, are refused; nothing is created.
	if st, out := e.post(b, "/api/v1/setup/begin", map[string]string{"code": "AAAAA-AAAAA-AAAAA-AAAAA-AAAAA", "username": "ada", "password": goodPassword}); st != 403 || out["code"] != "setup_code_invalid" {
		t.Fatalf("wrong code: %d %v", st, out)
	}
	if st, out := e.post(b, "/api/v1/setup/begin", map[string]string{"code": e.code, "username": "ada", "password": "password1234"}); st != 422 || out["code"] != "weak_password" {
		t.Fatalf("weak password: %d %v", st, out)
	}
	if st, out := e.post(b, "/api/v1/setup/begin", map[string]string{"code": e.code, "username": "A d", "password": goodPassword}); st != 422 || out["code"] != "invalid_username" {
		t.Fatalf("bad name: %d %v", st, out)
	}
	// The second factor is required for the administrator: a wrong code creates nothing.
	_, begin := e.post(b, "/api/v1/setup/begin", map[string]string{"code": e.code, "username": "ada", "display_name": "Ada", "password": goodPassword})
	if st, out := e.post(b, "/api/v1/setup/complete", map[string]string{"pending": begin["pending"].(string), "code": "000000"}); st != 422 || out["code"] != "invalid_code" {
		t.Fatalf("wrong totp: %d %v", st, out)
	}
	if n, _ := e.eng.CountLocalAccounts(); n != 0 {
		t.Fatal("an account was created without the second factor")
	}
	secret, codes := e.setUpAdmin(t, b, "ada")
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %v", codes)
	}
	// Signed in as administrator by the session the setup started.
	st, me := func() (int, meView) {
		r := b.get(e.srv.URL + "/api/v1/me")
		var m meView
		r.json(t, &m)
		return r.status, m
	}()
	if st != 200 || me.User != "ada" || !me.Admin || me.AuthMode != "local" {
		t.Fatalf("me %d %+v", st, me)
	}
	// The code is spent: setup cannot be run again, with it or any other.
	if st, out := e.post(newBrowser(t), "/api/v1/setup/begin", map[string]string{"code": e.code, "username": "eve", "password": goodPassword}); st != 409 || out["code"] != "setup_done" {
		t.Fatalf("setup again: %d %v", st, out)
	}
	_ = secret
}

// The session cookie is HttpOnly, SameSite and (behind HTTPS) Secure, and a new one is made at every sign-in.
func TestLocalSessionCookieAttributes(t *testing.T) {
	e := setupLocal(t, nil)
	b := newBrowser(t)
	_, begin := e.post(b, "/api/v1/setup/begin", map[string]string{"code": e.code, "username": "ada", "password": goodPassword})
	raw, _ := json.Marshal(map[string]string{"pending": begin["pending"].(string), "code": e.totp(t, begin["totp_secret"].(string))})
	r := b.do(http.MethodPost, e.srv.URL+"/api/v1/setup/complete", string(raw), "X-Forwarded-Proto", "https")
	var found *http.Cookie
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == sessionCookie {
			found = c
		}
	}
	if found == nil || !found.HttpOnly || found.SameSite != http.SameSiteLaxMode || found.Path != "/" || found.MaxAge != int((24*time.Hour)/time.Second) {
		t.Fatalf("cookie %+v", found)
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Fatal("a sign-in answer may be cached")
	}
}

func TestLocalSignInSecondFactorLockoutAndSignOut(t *testing.T) {
	e := setupLocal(t, nil)
	first := newBrowser(t)
	secret, recovery := e.setUpAdmin(t, first, "ada")

	b := newBrowser(t)
	// Wrong passwords say the same thing for a real and an unknown account.
	_, real := e.post(b, "/api/v1/auth/local/signin", map[string]string{"username": "ada", "password": "wrong wrong wrong wrong"})
	_, fake := e.post(b, "/api/v1/auth/local/signin", map[string]string{"username": "nobody", "password": "wrong wrong wrong wrong"})
	if real["error"] != fake["error"] || real["code"] != "invalid_credentials" {
		t.Fatalf("%v vs %v", real, fake)
	}
	st, step1 := e.post(b, "/api/v1/auth/local/signin", map[string]string{"username": "ada", "password": goodPassword})
	if st != 200 || step1["second_factor"] != "totp" || step1["signed_in"] != nil {
		t.Fatalf("step one: %d %v", st, step1)
	}
	if r := b.get(e.srv.URL + "/api/v1/me"); r.status != 401 {
		t.Fatalf("signed in after only the password: %d", r.status)
	}
	// The code from setup was already used in its step; the next step's code works once.
	e.clk.Add(31 * time.Second)
	code := e.totp(t, secret)
	if st, out := e.post(b, "/api/v1/auth/local/second-factor", map[string]string{"ticket": step1["ticket"].(string), "code": code}); st != 200 || out["signed_in"] != true {
		t.Fatalf("second factor: %d %v", st, out)
	}
	if r := b.get(e.srv.URL + "/api/v1/me"); r.status != 200 {
		t.Fatalf("me: %d", r.status)
	}
	// The same code never works twice (replay).
	b2 := newBrowser(t)
	_, s1 := e.post(b2, "/api/v1/auth/local/signin", map[string]string{"username": "ada", "password": goodPassword})
	if st, out := e.post(b2, "/api/v1/auth/local/second-factor", map[string]string{"ticket": s1["ticket"].(string), "code": code}); st != 401 || out["code"] != "invalid_code" {
		t.Fatalf("replayed code: %d %v", st, out)
	}
	// A recovery code works once.
	rc := recovery[0].(string)
	if st, out := e.post(b2, "/api/v1/auth/local/second-factor", map[string]string{"ticket": s1["ticket"].(string), "recovery_code": rc}); st != 200 {
		t.Fatalf("recovery code: %d %v", st, out)
	}
	b3 := newBrowser(t)
	_, s3 := e.post(b3, "/api/v1/auth/local/signin", map[string]string{"username": "ada", "password": goodPassword})
	if st, _ := e.post(b3, "/api/v1/auth/local/second-factor", map[string]string{"ticket": s3["ticket"].(string), "recovery_code": rc}); st != 401 {
		t.Fatalf("a recovery code worked twice: %d", st)
	}
	// Repeated failures lock the account, with a Retry-After, even for the right password.
	b4 := newBrowser(t)
	for i := 0; i < 6; i++ {
		e.post(b4, "/api/v1/auth/local/signin", map[string]string{"username": "ada", "password": "wrong wrong wrong wrong"})
	}
	raw, _ := json.Marshal(map[string]string{"username": "ada", "password": goodPassword})
	r := b4.do(http.MethodPost, e.srv.URL+"/api/v1/auth/local/signin", string(raw))
	if r.status != 429 || r.header.Get("Retry-After") == "" {
		t.Fatalf("a locked account: %d %v", r.status, r.header)
	}
	e.clk.Add(2 * time.Minute)
	if st, out := e.post(b4, "/api/v1/auth/local/signin", map[string]string{"username": "ada", "password": goodPassword}); st != 200 {
		t.Fatalf("after the lock: %d %v", st, out)
	}
	// Sign out everywhere ends every session of the person.
	if st, out := e.post(first, "/api/v1/account/sign-out-everywhere", map[string]string{}); st != 200 || out["ended"].(float64) < 2 {
		t.Fatalf("sign out everywhere: %d %v", st, out)
	}
	if r := b.get(e.srv.URL + "/api/v1/me"); r.status != 401 {
		t.Fatalf("a session survived: %d", r.status)
	}
	// Without the CSRF header a sign-in is refused.
	rr := b.do(http.MethodPost, e.srv.URL+"/api/v1/auth/local/signin", string(raw), csrfHeader, "")
	if rr.status != 403 {
		t.Fatalf("no CSRF header: %d", rr.status)
	}
	// Everything is in the audit trail.
	got := auditActions(t, e.eng)
	for _, want := range []string{"auth.setup", "auth.local.sign_in", "auth.local.second_factor", "auth.sign_out_everywhere"} {
		if !got[want] {
			t.Errorf("no %s audit entry (have %v)", want, got)
		}
	}
}

func auditActions(t *testing.T, eng *engine.Engine) map[string]bool {
	t.Helper()
	es, err := eng.AuditLog(engine.AuditFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, x := range es {
		out[x.Action] = true
	}
	return out
}

func TestInviteResetAndLastAdmin(t *testing.T) {
	e := setupLocal(t, nil)
	admin := newBrowser(t)
	e.setUpAdmin(t, admin, "ada")

	// The last administrator cannot be removed, disabled or demoted.
	for _, body := range []string{`{"role":"viewer"}`, `{"disabled":true}`} {
		r := admin.do(http.MethodPost, e.srv.URL+"/api/v1/users/ada", body)
		if r.status != 409 || !strings.Contains(string(r.body), "last_administrator") {
			t.Fatalf("%s: %d %s", body, r.status, r.body)
		}
	}
	if r := admin.do(http.MethodDelete, e.srv.URL+"/api/v1/users/ada", ""); r.status != 409 {
		t.Fatalf("delete last admin: %d", r.status)
	}
	// Invite a viewer: a one-time link, then they choose a password (no second factor needed for a viewer).
	st, inv := e.post(admin, "/api/v1/users", map[string]string{"username": "vic", "role": "viewer", "display_name": "Vic"})
	if st != 200 {
		t.Fatalf("invite: %d %v", st, inv)
	}
	token := strings.TrimPrefix(inv["path"].(string), "/accept?token=")
	vic := newBrowser(t)
	if st, out := e.post(vic, "/api/v1/auth/local/signin", map[string]string{"username": "vic", "password": goodPassword}); st != 401 {
		t.Fatalf("an invited person with no password signed in: %d %v", st, out)
	}
	if st, _ := e.post(vic, "/api/v1/auth/link", map[string]string{"token": "NOPE"}); st != 404 {
		t.Fatal("a wrong link was accepted")
	}
	_, begin := e.post(vic, "/api/v1/auth/link/begin", map[string]string{"token": token, "password": goodPassword})
	if begin["second_factor_required"] != false {
		t.Fatalf("a viewer needs no second factor by default: %v", begin)
	}
	if st, out := e.post(vic, "/api/v1/auth/link/complete", map[string]string{"pending": begin["pending"].(string)}); st != 200 || out["signed_in"] != true {
		t.Fatalf("accept: %d %v", st, out)
	}
	// The link works once.
	if st, _ := e.post(newBrowser(t), "/api/v1/auth/link/begin", map[string]string{"token": token, "password": goodPassword}); st != 404 {
		t.Fatalf("a used link: %d", st)
	}
	// A viewer may not use the user routes.
	if r := vic.get(e.srv.URL + "/api/v1/users"); r.status != 403 {
		t.Fatalf("viewer listing users: %d", r.status)
	}
	// Promoting to administrator needs a second factor they do not have.
	if r := admin.do(http.MethodPost, e.srv.URL+"/api/v1/users/vic", `{"role":"admin"}`); r.status != 409 || !strings.Contains(string(r.body), "second_factor_required") {
		t.Fatalf("promote without a factor: %d %s", r.status, r.body)
	}
	// A reset link lets a locked-out person back in with a new password.
	st, rl := e.post(admin, "/api/v1/users/vic/reset", map[string]string{})
	if st != 200 {
		t.Fatal(rl)
	}
	rtoken := strings.TrimPrefix(rl["path"].(string), "/accept?token=")
	_, begin = e.post(newBrowser(t), "/api/v1/auth/link/begin", map[string]string{"token": rtoken, "password": "another unrelated passphrase"})
	nb := newBrowser(t)
	if st, _ := e.post(nb, "/api/v1/auth/link/complete", map[string]string{"pending": begin["pending"].(string)}); st != 200 {
		t.Fatalf("reset complete: %d", st)
	}
	if st, _ := e.post(newBrowser(t), "/api/v1/auth/local/signin", map[string]string{"username": "vic", "password": goodPassword}); st != 401 {
		t.Fatal("the old password still works after a reset")
	}
	// The user list shows no secrets.
	r := admin.get(e.srv.URL + "/api/v1/users")
	if strings.Contains(string(r.body), "argon2") || strings.Contains(string(r.body), "totp_secret") {
		t.Fatalf("secrets in the list: %s", r.body)
	}
}

func TestSetupCodeFileExpiryAndNewCode(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "code")
	fileCode := localauth.NewSetupCode()
	if err := os.WriteFile(f, []byte(fileCode+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := setupLocal(t, func(l *LocalAuth) { l.SetupCodeFile = f; l.SetupTTL = time.Hour })
	if e.code != "" {
		t.Fatal("a code from a file was returned for printing")
	}
	b := newBrowser(t)
	e.clk.Add(2 * time.Hour)
	if st, out := e.post(b, "/api/v1/setup/begin", map[string]string{"code": fileCode, "username": "ada", "password": goodPassword}); st != 403 {
		t.Fatalf("an expired code: %d %v", st, out)
	}
	var status map[string]any
	b.get(e.srv.URL + "/api/v1/setup").json(t, &status)
	if status["required"] != true || status["code_expired"] != true {
		t.Fatalf("status %v", status)
	}
	// The console issues a new one, and the old one stops working.
	fresh, err := e.api.NewSetupCode("console")
	if err != nil {
		t.Fatal(err)
	}
	e.code = fresh
	e.setUpAdmin(t, b, "ada")
	if _, err := e.api.NewSetupCode("console"); err == nil {
		t.Fatal("a setup code was issued after setup")
	}
}
