package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// An installation with no sign-in on a reachable address (an old explicit
// opt-in) keeps working exactly as before: every call is answered, API
// tokens and the runner token are untouched. It says so in /me and /info and
// offers "set up sign-in now", which issues a one-time setup code once and
// records the request for the next start, and nothing else changes until
// then.
func TestOpenInstallKeepsWorkingWarnsAndOffersSignIn(t *testing.T) {
	clk := &testClock{t: testClockStart}
	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Engine: eng, RunnerToken: "runner-secret", Log: quiet(), Now: clk.Now, OpenInstall: true}
	s.Settings = testSettings(t, eng)
	s.Variables = testVariables(eng)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); eng.Close() })
	b := newBrowser(t)

	// Everything still answers, with no credential.
	for _, p := range []string{"/api/v1/me", "/api/v1/projects", "/api/v1/runs"} {
		if r := b.get(srv.URL + p); r.status != http.StatusOK {
			t.Fatalf("%s on an open installation: %d", p, r.status)
		}
	}
	var me map[string]any
	b.get(srv.URL+"/api/v1/me").json(t, &me)
	warn, _ := me["sign_in_off"].(map[string]any)
	if me["auth_mode"] != "none" || warn == nil || !strings.Contains(warn["warning"].(string), "no sign-in") || warn["next"] == "" {
		t.Fatalf("/me: %v", me)
	}
	var info map[string]any
	b.get(srv.URL+"/api/v1/info").json(t, &info)
	if info["sign_in_off"] == nil {
		t.Fatalf("/info: %v", info)
	}

	// The one click: refused without the page's header, otherwise a code once and the request recorded.
	if st := b.do(http.MethodPost, srv.URL+"/api/v1/auth/setup-now", "{}", csrfHeader, "").status; st != http.StatusForbidden {
		t.Fatalf("without the page's header: %d", st)
	}
	r := b.do(http.MethodPost, srv.URL+"/api/v1/auth/setup-now", "{}", csrfHeader, "1")
	var out map[string]any
	r.json(t, &out)
	code, _ := out["setup_code"].(string)
	if r.status != 200 || strings.Count(code, "-") != 4 || !strings.Contains(out["next"].(string), "Restart the servers") {
		t.Fatalf("setup-now: %d %v", r.status, out)
	}
	if _, asked, err := eng.LocalSignInRequested(); err != nil || !asked {
		t.Fatalf("the request was not recorded: %v %v", asked, err)
	}
	st, err := eng.SetupCodeState()
	if err != nil || st == nil || st.UsedAt != 0 || st.ExpiresAt <= clk.Now().UnixMilli() || st.ExpiresAt > clk.Now().Add(25*time.Hour).UnixMilli() {
		t.Fatalf("the setup code: %+v %v", st, err)
	}
	// Until the servers restart nothing else changed: still open.
	if r := b.get(srv.URL + "/api/v1/projects"); r.status != http.StatusOK {
		t.Fatalf("after the request, before a restart: %d", r.status)
	}
	var me2 map[string]any
	b.get(srv.URL+"/api/v1/me").json(t, &me2)
	if w, _ := me2["sign_in_off"].(map[string]any); w == nil || w["requested_at"] == nil {
		t.Fatalf("the warning does not show the request: %v", me2)
	}

	// An installation that has sign-in, or one that is not open (loopback), has neither the warning nor the route.
	s2 := &Server{Engine: eng, RunnerToken: "r", Log: quiet(), Now: clk.Now}
	s2.Settings, s2.Variables = s.Settings, s.Variables
	srv2 := httptest.NewServer(s2.Handler())
	defer srv2.Close()
	var me3 map[string]any
	b.get(srv2.URL+"/api/v1/me").json(t, &me3)
	if me3["sign_in_off"] != nil {
		t.Fatalf("a loopback-only server warned: %v", me3)
	}
	if st := b.do(http.MethodPost, srv2.URL+"/api/v1/auth/setup-now", "{}", csrfHeader, "1").status; st != http.StatusNotFound {
		t.Fatalf("setup-now on a closed server: %d", st)
	}
}
