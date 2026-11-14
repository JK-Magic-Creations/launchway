package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/fakeidp"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// Two servers share one PostgreSQL database behind a load balancer, which
// may send each request of a browser to either. Sign-in state lives only in
// the database and the browser, so it does not matter which.
func TestSignInAcrossServers(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	db := testdb.New(t)
	idp := fakeidp.New()
	t.Cleanup(idp.Close)
	clk := &testClock{t: testClockStart}
	// The provider sends the browser back to B, as a load balancer might.
	b := setupAuth(t, envOpts{db: db, idp: idp, clk: clk})
	a := setupAuth(t, envOpts{db: db, idp: idp, clk: clk, redirectURL: b.srv.URL + "/auth/callback"})
	if a.api.Engine == b.api.Engine {
		t.Fatal("the servers share an engine")
	}

	// A sign-in started on A finishes on B. The two test servers share a
	// host, so the browser sends its cookies to both, as it would to one
	// address in front of both.
	idp.SignInAs("otto", "pay-ops")
	br := newBrowser(t)
	callback := br.authorize(br.beginLogin(a.srv.URL, "/approvals"))
	if !strings.HasPrefix(callback, b.srv.URL+"/auth/callback?") {
		t.Fatalf("the provider sent the browser to %s", callback)
	}
	if r := br.get(callback); r.status != http.StatusFound || r.location() != "/approvals" {
		t.Fatalf("callback on B: %d %s %s", r.status, r.location(), r.body)
	}

	// The session made on B works on A.
	for _, e := range []*authEnv{a, b} {
		if code, m := e.me(t, br); code != http.StatusOK || m.User != "otto" {
			t.Fatalf("me on %s: %d %+v", e.srv.URL, code, m)
		}
	}

	// A token made on A works on B, and a token revoked on B stops on A.
	id, tok := br.newToken(a.srv.URL, "ci")
	cli := newBrowser(t)
	if r := cli.do(http.MethodPost, b.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`, bearer(tok)...); r.status != http.StatusCreated {
		t.Fatalf("token from A used on B: %d %s", r.status, r.body)
	}
	if r := br.do(http.MethodDelete, b.srv.URL+"/api/v1/tokens/"+id, ""); r.status != http.StatusNoContent {
		t.Fatalf("revoke on B: %d", r.status)
	}
	if code, _ := a.me(t, cli, bearer(tok)...); code != http.StatusUnauthorized {
		t.Fatalf("revoked token on A: %d", code)
	}

	// Signing out on B ends the session on A.
	if r := br.do(http.MethodPost, b.srv.URL+"/auth/logout", ""); r.status != http.StatusNoContent {
		t.Fatalf("logout on B: %d", r.status)
	}
	raw := br.cookieValue(a.srv.URL, sessionCookie)
	if raw != "" {
		t.Fatal("the browser kept the session cookie")
	}
	// Even a copy of the cookie kept from before is refused on A.
	idp.SignInAs("vic", "pay-view")
	br2 := newBrowser(t)
	br2.get(br2.authorize(br2.beginLogin(a.srv.URL, "/")))
	kept := br2.cookieValue(a.srv.URL, sessionCookie)
	br2.do(http.MethodPost, b.srv.URL+"/auth/logout", "")
	thief := newBrowser(t)
	req, _ := http.NewRequest(http.MethodGet, a.srv.URL+"/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: kept})
	resp, err := thief.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a session ended on B still works on A: %d", resp.StatusCode)
	}

	// Both servers wrote to the same audit trail.
	entries := a.auditEntries(t)
	if len(entries) < 6 {
		t.Fatalf("audit trail %+v", entries)
	}
}
