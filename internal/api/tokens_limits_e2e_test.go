package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// makeToken makes a token as the browser's user with the given request
// body, and returns its ID and value.
func (b *browser) makeToken(base, body string) (string, string, response) {
	b.t.Helper()
	r := b.do(http.MethodPost, base+"/api/v1/tokens", body)
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if r.status == http.StatusCreated {
		r.json(b.t, &out)
	}
	return out.ID, out.Token, r
}

func TestLimitedTokens(t *testing.T) {
	e := setupAuth(t, envOpts{})
	base := e.srv.URL
	ada := e.signIn(t, "ada") // admin everywhere
	otto := e.signIn(t, "otto")

	// A role higher than its maker's is refused; so are bad patterns.
	if _, _, r := otto.makeToken(base, `{"name":"x","role":"admin"}`); r.status != 400 || !strings.Contains(string(r.body), "no higher than yours") {
		t.Fatalf("otto making an admin token: %d %s", r.status, r.body)
	}
	if _, _, r := otto.makeToken(base, `{"name":"x","projects":["[bad"]}`); r.status != 400 {
		t.Fatalf("bad pattern: %d", r.status)
	}
	if _, _, r := otto.makeToken(base, `{"name":"x","expires_in_days":366}`); r.status != 400 {
		t.Fatalf("beyond the maximum: %d", r.status)
	}

	// Ada's token limited to viewer on pay-*: she reads pay-api, but
	// cannot operate it, and web is as if it did not exist.
	_, viewTok, r := ada.makeToken(base, `{"name":"ci reader","role":"viewer","projects":["pay-*"],"expires_in_days":30}`)
	if r.status != 201 {
		t.Fatalf("making a limited token: %d %s", r.status, r.body)
	}
	cli := newBrowser(t)
	pay := e.startRun(t, "pay-api")
	web := e.startRun(t, "web")
	check := func(method, path, body string, want int) {
		t.Helper()
		if got := cli.statusOf(method, base+path, body, bearer(viewTok)...); got != want {
			t.Errorf("%s %s with the limited token: %d, want %d", method, path, got, want)
		}
	}
	check("GET", "/api/v1/runs/"+pay, "", 200)
	check("GET", "/api/v1/runs/"+web, "", 404)
	check("POST", "/api/v1/runs/"+pay+"/cancel", "", 403)
	check("POST", "/api/v1/projects/pay-api/runs", `{"ref":"main"}`, 403)
	check("GET", "/api/v1/audit", "", 403) // admin of every project, but the token is not
	var runs []engine.RunSummary
	cli.get(base+"/api/v1/runs", bearer(viewTok)...).json(t, &runs)
	if len(runs) != 1 || runs[0].ID != pay {
		t.Fatalf("the limited token lists %d runs", len(runs))
	}
	var me struct {
		Roles map[string]string `json:"roles"`
		Admin bool              `json:"admin"`
	}
	cli.get(base+"/api/v1/me", bearer(viewTok)...).json(t, &me)
	if me.Admin || me.Roles["pay-api"] != "viewer" || me.Roles["web"] != "" {
		t.Fatalf("me with the limited token: %+v", me)
	}
	// Denials of a token on routes that are not audited are audited as
	// token.use.
	found := false
	for _, a := range e.auditEntries(t) {
		if a.Action == "token.use" && a.Outcome == "denied" && a.User == "ada" && strings.Contains(a.Detail, "/api/v1/runs/"+web) {
			found = true
		}
	}
	if !found {
		t.Fatal("no audit entry for the limited token's denial")
	}

	// An operator token for pay-api only does what otto may there.
	_, opTok, _ := otto.makeToken(base, `{"name":"deployer","role":"operator","projects":["pay-api"]}`)
	if got := cli.statusOf("POST", base+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`, bearer(opTok)...); got != 201 {
		t.Fatalf("operator token starting a run: %d", got)
	}

	// Listing shows the limits and where it was last used from.
	var list []engine.APIToken
	ada.get(base+"/api/v1/tokens").json(t, &list)
	if len(list) != 1 || list[0].Role != "viewer" || list[0].Projects[0] != "pay-*" || list[0].LastUsedFrom != "127.0.0.1" || list[0].LastUsed == 0 {
		t.Fatalf("ada's tokens: %+v", list)
	}
	// Admins see and revoke anyone's; others may not.
	var all []engine.APIToken
	ada.get(base+"/api/v1/admin/tokens").json(t, &all)
	var ottos string
	for _, tk := range all {
		if tk.OwnerName == "otto" {
			ottos = tk.ID
		}
	}
	if len(all) != 2 || ottos == "" {
		t.Fatalf("all tokens: %+v", all)
	}
	if got := otto.statusOf("DELETE", base+"/api/v1/admin/tokens/"+ottos, ""); got != 403 {
		t.Fatalf("otto using the admin route: %d", got)
	}
	if got := ada.statusOf("DELETE", base+"/api/v1/admin/tokens/"+ottos, ""); got != 204 {
		t.Fatalf("ada revoking otto's token: %d", got)
	}
	if got := cli.statusOf("GET", base+"/api/v1/runs", "", bearer(opTok)...); got != 401 {
		t.Fatalf("a revoked token: %d", got)
	}
	revoked := false
	for _, a := range e.auditEntries(t) {
		if a.Action == "token.revoke" && a.User == "ada" && a.Detail == "a token of otto" && a.Outcome == "ok" {
			revoked = true
		}
	}
	if !revoked {
		t.Fatal("the admin's revocation is not in the audit trail")
	}

	// An expired token is refused and the refusal recorded.
	e.clk.Add(31 * 24 * time.Hour)
	if got := cli.statusOf("GET", base+"/api/v1/runs", "", bearer(viewTok)...); got != 401 {
		t.Fatalf("an expired token: %d", got)
	}
	expired := false
	for _, a := range e.auditEntries(t) {
		if a.Action == "token.use" && a.User == "ada" && a.Target == "ci reader" && strings.Contains(a.Detail, "has expired") {
			expired = true
		}
	}
	if !expired {
		t.Fatal("no audit entry for the expired token")
	}
}

// A limited token's hash is one an older server never finds.
func TestLimitedTokensAreStoredApart(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	_, tok, _ := ada.makeToken(e.srv.URL, `{"name":"limited","role":"viewer"}`)
	_, plain, _ := ada.makeToken(e.srv.URL, `{"name":"plain"}`)
	if _, _, err := e.eng.TokenByHash(auth.Hash(tok)); err == nil {
		t.Fatal("an older server's lookup found the limited token")
	}
	if _, _, err := e.eng.TokenByHash(auth.Hash(plain)); err != nil {
		t.Fatalf("an older server's lookup of a plain token: %v", err)
	}
}

func TestFailedAuthenticationIsLimited(t *testing.T) {
	e := setupAuth(t, envOpts{})
	ada := e.signIn(t, "ada")
	_, good, _ := ada.makeToken(e.srv.URL, `{"name":"good"}`)
	b := newBrowser(t)
	for i := range maxFailuresPerWindow {
		if got := b.statusOf("GET", e.srv.URL+"/api/v1/runs", "", bearer(fmt.Sprintf("lw_wrong%d", i))...); got != 401 {
			t.Fatalf("failure %d: %d", i, got)
		}
	}
	// Now even a good token from this address waits, with Retry-After.
	r := b.do("GET", e.srv.URL+"/api/v1/runs", "", bearer(good)...)
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("after %d failures: %d %v", maxFailuresPerWindow, r.status, r.header)
	}
	// Signed-in browsers (no Authorization header) are not affected.
	if got := ada.statusOf("GET", e.srv.URL+"/api/v1/runs", ""); got != 200 {
		t.Fatalf("a session from the same address: %d", got)
	}
	// The flood is recorded once, not once per attempt.
	n := 0
	for _, a := range e.auditEntries(t) {
		if a.Action == "token.use" && a.Outcome == "denied" {
			n++
		}
	}
	if n != maxFailuresPerWindow {
		t.Fatalf("%d audit entries for %d failures and a refused attempt", n, maxFailuresPerWindow)
	}
	// After the window, it may try again.
	e.clk.Add(failureWindow + time.Second)
	if got := b.statusOf("GET", e.srv.URL+"/api/v1/runs", "", bearer(good)...); got != 200 {
		t.Fatalf("after the window: %d", got)
	}
}

// Behind a trusted proxy the address is the one the proxy saw; without
// trust_proxy, X-Forwarded-For is ignored, so a client cannot dodge the
// limit by inventing addresses.
func TestFailureLimitAddress(t *testing.T) {
	for _, trust := range []bool{false, true} {
		e := setupAuth(t, envOpts{trustProxy: trust})
		b := newBrowser(t)
		for i := range maxFailuresPerWindow {
			b.statusOf("GET", e.srv.URL+"/api/v1/runs", "", "Authorization", "Bearer lw_x", "X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i))
		}
		got := b.statusOf("GET", e.srv.URL+"/api/v1/runs", "", "Authorization", "Bearer lw_x", "X-Forwarded-For", "10.0.0.250")
		want := 429
		if trust {
			want = 401 // each address failed once
		}
		if got != want {
			t.Errorf("trust_proxy %v: %d, want %d", trust, got, want)
		}
	}
}

func TestFailureLimiterIsBounded(t *testing.T) {
	l := &failureLimiter{window: time.Minute, max: 3, sources: map[string]*failures{}}
	now := time.Now()
	for i := range maxSources + 500 {
		l.fail(fmt.Sprintf("10.%d.%d.%d", i>>16, (i>>8)&255, i&255), now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(l.sources) > maxSources {
		t.Fatalf("%d sources kept", len(l.sources))
	}
	for range 3 {
		l.fail("192.0.2.1", now)
	}
	if l.blocked("192.0.2.1", now) == 0 {
		t.Fatal("not blocked")
	}
	if l.blocked("192.0.2.1", now.Add(time.Minute)) != 0 {
		t.Fatal("still blocked after the window")
	}
}
