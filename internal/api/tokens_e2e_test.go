package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func TestAPITokens(t *testing.T) {
	e := setupAuth(t, envOpts{})
	otto := e.signIn(t, "otto")
	created := e.clk.Now()

	// Made once, shown once.
	r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/tokens", `{"name": "deploy bot"}`)
	if r.status != http.StatusCreated || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %v %s", r.status, r.header, r.body)
	}
	var made struct {
		engine.APIToken
		Token string `json:"token"`
	}
	r.json(t, &made)
	if !strings.HasPrefix(made.Token, "lw_") || len(made.Token) != 46 || made.Name != "deploy bot" ||
		made.ExpiresAt != created.Add(90*24*time.Hour).UnixMilli() {
		t.Fatalf("made %+v", made)
	}
	list := otto.get(e.srv.URL + "/api/v1/tokens")
	if list.status != http.StatusOK || strings.Contains(string(list.body), made.Token) || !strings.Contains(string(list.body), made.ID) {
		t.Fatalf("list: %d %s", list.status, list.body)
	}

	// It works on its own, with the owner's rights.
	cli := newBrowser(t)
	code, m := e.me(t, cli, bearer(made.Token)...)
	if code != http.StatusOK || m.User != "otto" || m.Via != "token" || m.Roles["pay-api"] != "operator" {
		t.Fatalf("me with the token: %d %+v", code, m)
	}
	if r := cli.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`, bearer(made.Token)...); r.status != http.StatusCreated {
		t.Fatalf("starting a run with the token: %d %s", r.status, r.body)
	}
	if r := cli.get(e.srv.URL+"/api/v1/projects/web", bearer(made.Token)...); r.status != http.StatusNotFound {
		t.Fatalf("a project otto may not see: %d", r.status)
	}

	// A request naming a token is judged by the token alone, even with a
	// valid session cookie.
	if code, _ := e.me(t, otto, bearer("lw_not-a-real-token")...); code != http.StatusUnauthorized {
		t.Fatalf("a bad token with a good cookie: %d", code)
	}
	if code, _ := e.me(t, otto, "Authorization", "Basic b3R0bzp4"); code != http.StatusUnauthorized {
		t.Fatalf("another kind of credential: %d", code)
	}

	// last_used is written at most once a minute.
	lastUsed := func() int64 {
		var l []engine.APIToken
		otto.get(e.srv.URL+"/api/v1/tokens").json(t, &l)
		return l[0].LastUsed
	}
	first := lastUsed()
	if first != created.UnixMilli() {
		t.Fatalf("last used %d, want %d", first, created.UnixMilli())
	}
	e.clk.Add(30 * time.Second)
	e.me(t, cli, bearer(made.Token)...)
	if lastUsed() != first {
		t.Fatal("last_used was written twice within a minute")
	}
	e.clk.Add(31 * time.Second)
	e.me(t, cli, bearer(made.Token)...)
	if lastUsed() != e.clk.Now().UnixMilli() {
		t.Fatal("last_used was not written after a minute")
	}

	// A token cannot make tokens: a stolen one could otherwise renew itself.
	if r := cli.do(http.MethodPost, e.srv.URL+"/api/v1/tokens", `{"name": "child"}`, bearer(made.Token)...); r.status != http.StatusForbidden {
		t.Fatalf("a token making a token: %d %s", r.status, r.body)
	}
	for body, want := range map[string]int{
		`{"name": ""}`:                               http.StatusBadRequest,
		`{"name": "x", "expires_in_days": 366}`:      http.StatusBadRequest,
		`{"name": "x", "expires_in_days": -1}`:       http.StatusBadRequest,
		`{"name": "deploy bot"}`:                     http.StatusConflict,
		`{"name": "a year", "expires_in_days": 365}`: http.StatusCreated,
	} {
		if r := otto.do(http.MethodPost, e.srv.URL+"/api/v1/tokens", body); r.status != want {
			t.Errorf("create %s: %d %s, want %d", body, r.status, r.body, want)
		}
	}

	// Rights are the owner's as of their latest sign-in.
	e.idp.SignInAs("otto", "pay-view")
	newBrowser(t).login(e.srv.URL, "/")
	if r := cli.do(http.MethodPost, e.srv.URL+"/api/v1/projects/pay-api/runs", `{"ref":"main"}`, bearer(made.Token)...); r.status != http.StatusForbidden {
		t.Fatalf("the token kept rights its owner lost: %d", r.status)
	}

	// Someone else cannot revoke it, or see it.
	vic := e.signIn(t, "vic")
	if r := vic.do(http.MethodDelete, e.srv.URL+"/api/v1/tokens/"+made.ID, ""); r.status != http.StatusNotFound {
		t.Fatalf("vic revoking otto's token: %d", r.status)
	}
	if r := vic.get(e.srv.URL + "/api/v1/tokens"); string(r.body) != "[]\n" {
		t.Fatalf("vic sees %s", r.body)
	}

	// Only the hash is stored.
	dump := dumpDatabase(t, e.db)
	if strings.Contains(dump, made.Token) || strings.Contains(dump, strings.TrimPrefix(made.Token, "lw_")) {
		t.Fatal("the token is stored in the database")
	}
	if !strings.Contains(dump, auth.Hash(made.Token)) {
		t.Fatal("the token's hash is not stored")
	}

	// Revoked: gone at once.
	if r := otto.do(http.MethodDelete, e.srv.URL+"/api/v1/tokens/"+made.ID, ""); r.status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", r.status, r.body)
	}
	if code, _ := e.me(t, cli, bearer(made.Token)...); code != http.StatusUnauthorized {
		t.Fatalf("a revoked token: %d", code)
	}
	if r := otto.do(http.MethodDelete, e.srv.URL+"/api/v1/tokens/"+made.ID, ""); r.status != http.StatusNotFound {
		t.Fatalf("revoking twice: %d", r.status)
	}
}

func TestAPITokensExpire(t *testing.T) {
	e := setupAuth(t, envOpts{})
	b := e.signIn(t, "vic")
	r := b.do(http.MethodPost, e.srv.URL+"/api/v1/tokens", `{"name": "short", "expires_in_days": 1}`)
	var made struct {
		Token string `json:"token"`
	}
	r.json(t, &made)
	cli := newBrowser(t)
	e.clk.Add(24*time.Hour - time.Second)
	if code, _ := e.me(t, cli, bearer(made.Token)...); code != http.StatusOK {
		t.Fatalf("before expiry: %d", code)
	}
	e.clk.Add(time.Second)
	if code, _ := e.me(t, cli, bearer(made.Token)...); code != http.StatusUnauthorized {
		t.Fatalf("at expiry: %d", code)
	}
}

func TestTokensNeedSignIn(t *testing.T) {
	e := setup(t, time.Second)
	resp, err := http.Get(e.srv.URL + "/api/v1/tokens")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("tokens without sign-in: %d", resp.StatusCode)
	}
}
