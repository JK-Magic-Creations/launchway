package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

type svcToken struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// A service account owns tokens made without a person's browser; only an
// administrator's session may make them (an administrator's token only when
// the setting allows it), and they stop with the account.
func TestServiceAccountsAndTheirTokens(t *testing.T) {
	e, _ := hostedEnv(t, envOpts{})
	hostedRepo(t, e, "acme/shop")
	hostedRepo(t, e, "acme/lib")
	hostedRepo(t, e, "other/secret")
	ada, otto := e.signIn(t, "ada"), e.signIn(t, "otto")
	base, cli := e.srv.URL, newBrowser(t)

	if r := otto.do("POST", base+"/api/v1/service-accounts", `{"name":"gitops","role":"viewer"}`); r.status != 403 {
		t.Fatalf("a non-administrator making a service account: %d", r.status)
	}
	for _, bad := range []string{`{"name":"x"}`, `{"name":"Bad Name"}`, `{"name":"service:x"}`, `{"name":"gitops","role":"root"}`} {
		if r := ada.do("POST", base+"/api/v1/service-accounts", bad); r.status != 400 {
			t.Errorf("%s: %d", bad, r.status)
		}
	}
	if r := ada.do("POST", base+"/api/v1/service-accounts", `{"name":"gitops","description":"reads for the controller","role":"operator"}`); r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := ada.do("POST", base+"/api/v1/service-accounts", `{"name":"gitops"}`); r.status != 409 {
		t.Errorf("the same name twice: %d", r.status)
	}

	// Tokens for it: not by a non-administrator, not by an administrator's token.
	body := `{"name":"push-two","repositories":["acme/shop","acme/lib"],"repo_access":"write","source_ranges":["127.0.0.0/8"]}`
	if r := otto.do("POST", base+"/api/v1/service-accounts/gitops/tokens", body); r.status != 403 {
		t.Fatalf("a non-administrator making a service token: %d", r.status)
	}
	adminTok := e.tokenFor(t, "ada", "ada-api")
	if r := cli.do("POST", base+"/api/v1/service-accounts/gitops/tokens", body, bearer(adminTok)...); r.status != 403 || !strings.Contains(string(r.body), "service_accounts_from_tokens") {
		t.Fatalf("an administrator's token making a service token by default: %d %s", r.status, r.body)
	}
	if r := ada.do("POST", base+"/api/v1/service-accounts/nobody/tokens", body); r.status != 404 {
		t.Errorf("a missing account: %d", r.status)
	}
	// A role above the account's is refused.
	if r := ada.do("POST", base+"/api/v1/service-accounts/gitops/tokens", `{"name":"x","role":"admin"}`); r.status != 400 {
		t.Errorf("a token above its account: %d", r.status)
	}
	var made svcToken
	r := ada.do("POST", base+"/api/v1/service-accounts/gitops/tokens", body)
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r.json(t, &made)

	// The token acts as the account: write to its two repositories, nothing else.
	src := workingCopy(t)
	mustGit(t, src, "push", "-q", withToken(base, "/acme/shop", made.Token), "HEAD:refs/heads/main")
	if got := gitStatus(e, made.Token, "/other/secret", "git-upload-pack"); got != 404 {
		t.Errorf("the service token reaching another repository: %d", got)
	}
	if s := cli.statusOf("GET", base+"/api/v1/audit", "", bearer(made.Token)...); s != 403 {
		t.Errorf("the service token on the audit trail: %d", s)
	}
	// It cannot make tokens, for anyone, even with the setting on.
	e.api.AdminTokenMakesServiceTokens = true
	if r := cli.do("POST", base+"/api/v1/tokens", `{"name":"child"}`, bearer(made.Token)...); r.status != 403 {
		t.Errorf("a service token making a token: %d", r.status)
	}
	if r := cli.do("POST", base+"/api/v1/service-accounts/gitops/tokens", `{"name":"child"}`, bearer(made.Token)...); r.status != 403 {
		t.Errorf("a service token making a service token: %d", r.status)
	}
	// With the setting on, an administrator's token may.
	if r := cli.do("POST", base+"/api/v1/service-accounts/gitops/tokens", `{"name":"second"}`, bearer(adminTok)...); r.status != 201 {
		t.Errorf("an administrator's token with the setting on: %d %s", r.status, r.body)
	}
	e.api.AdminTokenMakesServiceTokens = false

	// An unlimited service token has the account's role and no more: operator
	// everywhere, not administrator.
	var plain svcToken
	ada.do("POST", base+"/api/v1/service-accounts/gitops/tokens", `{"name":"plain"}`).json(t, &plain)
	if s := cli.statusOf("GET", base+"/api/v1/audit", "", bearer(plain.Token)...); s != 403 {
		t.Errorf("an operator service token on the audit trail: %d", s)
	}
	if s := gitStatus(e, plain.Token, "/other/secret", "git-receive-pack"); s != 200 {
		t.Errorf("an operator service account pushing: %d", s)
	}
	// Audit: the account is the actor, the token is named, the maker is recorded.
	var sawPush, sawMint bool
	for _, a := range e.auditEntries(t) {
		sawPush = sawPush || (a.Action == "git.push" && a.User == "service:gitops" && a.TokenName == "push-two")
		sawMint = sawMint || (a.Action == "token.create" && a.User == "ada" && strings.Contains(a.Detail, "for service:gitops"))
	}
	if !sawPush || !sawMint {
		t.Errorf("audit: push %v, mint %v", sawPush, sawMint)
	}

	// Listing, switching off, on, deleting.
	var tl []engine.APIToken
	ada.get(base+"/api/v1/service-accounts/gitops/tokens").json(t, &tl)
	if len(tl) < 3 {
		t.Errorf("the account's tokens: %d", len(tl))
	}
	if r := ada.do("PATCH", base+"/api/v1/service-accounts/gitops", `{"disabled":true}`); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if s := gitStatus(e, made.Token, "/acme/shop", "git-upload-pack"); s != 401 {
		t.Errorf("a switched-off account's token: %d", s)
	}
	if r := ada.do("POST", base+"/api/v1/service-accounts/gitops/tokens", `{"name":"late"}`); r.status != 409 {
		t.Errorf("a token for a switched-off account: %d", r.status)
	}
	ada.do("PATCH", base+"/api/v1/service-accounts/gitops", `{"disabled":false,"role":"viewer"}`)
	if s := gitStatus(e, made.Token, "/acme/shop", "git-receive-pack"); s != 403 {
		t.Errorf("after the account became a viewer, a write token pushes: %d (the account's role caps it)", s)
	}
	if s := gitStatus(e, made.Token, "/acme/shop", "git-upload-pack"); s != 200 {
		t.Errorf("a viewer account reads: %d", s)
	}
	if r := ada.do("DELETE", base+"/api/v1/service-accounts/gitops", ""); r.status != 204 {
		t.Fatalf("%d", r.status)
	}
	if s := gitStatus(e, made.Token, "/acme/shop", "git-upload-pack"); s != 401 {
		t.Errorf("a deleted account's token: %d", s)
	}
	var left []engine.APIToken
	ada.get(base+"/api/v1/admin/tokens").json(t, &left)
	for _, x := range left {
		if strings.HasPrefix(x.OwnerName, "service:") {
			t.Errorf("a token of the deleted account is left: %+v", x)
		}
	}
	_ = http.StatusOK
}
