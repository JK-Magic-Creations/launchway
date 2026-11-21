package api

// Test-before-switch for an identity provider (the setup guide's sign-in step). The administrator
// starts a test, a second window takes them through the provider's own sign-in page, and the result is
// recorded in the audit trail (so every server of a highly available installation sees it, and so does
// the Logbook). Nothing about how people sign in changes: this server cannot change its sign-in while it
// runs (the provider is read from its settings file at start), so a successful test ends with the exact
// setting to add, and the built-in administrator stays as the way back in.

import (
	"crypto/subtle"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

const (
	testCookie = "launchway_signin_test"
	testPath   = "/auth/signin-test"
	testTTL    = 10 * time.Minute
	testAction = "signin.test_result"
)

type signinTestState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	By       string `json:"b"`
	Issuer   string `json:"i"`
	ClientID string `json:"c"`
	Secret   string `json:"x"` // the NAME of the environment variable, never the secret
	Redirect string `json:"r"`
	Groups   string `json:"g"`
	User     string `json:"u"`
	Expires  int64  `json:"e"`
}

func (s *Server) origin(r *http.Request) string {
	scheme := "http"
	if s.isHTTPS(r) || (s.Auth != nil && s.Auth.SecureCookies) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// POST /api/v1/signin/test: the administrator's answers; returns the address to open in a second window.
func (s *Server) signinTestBegin(w http.ResponseWriter, r *http.Request) {
	var in settings.SignInRequest
	if !decode(w, r, &in) || !s.throttleCheck(w, r) {
		return
	}
	setAudit(r, auditAddress(in.Issuer), "")
	fail := func(code int, c, msg, next string) {
		writeJSON(w, code, map[string]string{"error": msg, "code": c, "next": next})
	}
	in.RedirectURL = s.origin(r) + testPath
	in.Issuer = strings.TrimRight(strings.TrimSpace(in.Issuer), "/")
	in.ClientID = strings.TrimSpace(in.ClientID)
	if in.Issuer == "" || in.ClientID == "" {
		fail(http.StatusBadRequest, "invalid", "the provider's address (issuer) and the client id are both needed", "Fill both in from the application you made in the provider.")
		return
	}
	if _, problem, blocked := settings.DiscoverIssuer(r.Context(), in.Issuer); problem != "" {
		next := "Fix the address, then test again."
		if blocked {
			next = "If this is your own identity provider on your network, an administrator can allow its host (Settings, allowed hosts)."
		}
		fail(http.StatusBadRequest, "invalid", problem, next)
		return
	}
	secret := ""
	if in.ClientSecretEnv != "" {
		secret = os.Getenv(in.ClientSecretEnv)
		if secret == "" {
			fail(http.StatusConflict, "secret_missing", "the environment variable "+in.ClientSecretEnv+" is not set on this server",
				"Put the provider's client secret in that variable on every server and restart, or name the variable you did set.")
			return
		}
	}
	sealer, err := s.loginSealer()
	if err != nil {
		s.fail(w, err)
		return
	}
	st := signinTestState{State: auth.RandomString(32), Nonce: auth.RandomString(32), Verifier: oauth2.GenerateVerifier(), By: identityFrom(r).User,
		Issuer: in.Issuer, ClientID: in.ClientID, Secret: in.ClientSecretEnv, Redirect: in.RedirectURL, Groups: in.GroupsClaim, User: in.UsernameClaim,
		Expires: s.Now().Add(testTTL).UnixMilli()}
	o := s.testOIDC(st, secret)
	target, err := o.AuthURL(r.Context(), st.State, st.Nonce, st.Verifier)
	if err != nil {
		fail(http.StatusBadGateway, "provider_unreachable", "the identity provider could not be reached to start a sign-in", "Check the address and that this server can connect to it.")
		return
	}
	sealed, err := sealer.Seal(st)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, testCookie, sealed, testPath, testTTL))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"url": target, "redirect_url": in.RedirectURL, "expires_in_seconds": int(testTTL / time.Second)})
}

func (s *Server) testOIDC(st signinTestState, secret string) *auth.OIDC {
	return auth.NewOIDC(auth.OIDCOptions{Issuer: st.Issuer, ClientID: st.ClientID, ClientSecret: secret, RedirectURL: st.Redirect,
		GroupsClaim: st.Groups, UsernameClaim: st.User, HTTPClient: settings.GuardedHTTPClient(), Now: s.Now})
}

// GET /auth/signin-test: where the provider sends the second window back.
func (s *Server) signinTestCallback(w http.ResponseWriter, r *http.Request) {
	now := s.Now()
	http.SetCookie(w, s.cookie(r, testCookie, "", testPath, 0))
	w.Header().Set("Cache-Control", "no-store")
	say := func(ok bool, title, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		cls := map[bool]string{true: "ok", false: "bad"}[ok]
		fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>`+
			`<style>:root{color-scheme:light dark}body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:15vh auto;padding:0 16px}h1{font-size:1.3rem}.ok h1{color:#0b6b2e}.bad h1{color:#a31515}@media(prefers-color-scheme:dark){.ok h1{color:#6ee09a}.bad h1{color:#ff8a8a}}</style></head>`+
			`<body class="%s"><main><h1>%s</h1><p>%s</p></main></body></html>`, html.EscapeString(title), cls, html.EscapeString(title), html.EscapeString(body))
	}
	record := func(by, outcome, detail string) {
		s.record(&engine.AuditEntry{At: now.UnixMilli(), User: by, Source: s.clientAddr(r), Action: "signin.test_result", Outcome: outcome, Detail: detail})
	}
	c, err := r.Cookie(testCookie)
	if err != nil {
		say(false, "This test sign-in has expired", "Go back to the setup guide and start the test again, in the same browser.")
		return
	}
	sealer, err := s.loginSealer()
	if err != nil {
		s.fail(w, err)
		return
	}
	var st signinTestState
	if err := sealer.Open(c.Value, &st); err != nil || now.UnixMilli() >= st.Expires {
		say(false, "This test sign-in has expired", "Go back to the setup guide and start the test again.")
		return
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		record(st.By, "failed", "the state did not match the test started in this browser")
		say(false, "That did not match the test you started", "Start the test again from the setup guide.")
		return
	}
	if e := q.Get("error"); e != "" {
		record(st.By, "failed", "the provider refused: "+clip(e+" "+q.Get("error_description"), 200))
		say(false, "The provider refused the sign-in", "It said: "+clip(e+" "+q.Get("error_description"), 200)+". Check the application's settings in the provider and test again.")
		return
	}
	secret := ""
	if st.Secret != "" {
		secret = os.Getenv(st.Secret)
	}
	claims, err := s.testOIDC(st, secret).Exchange(r.Context(), q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		record(st.By, "failed", "the provider's answer was not accepted: "+clip(err.Error(), 200))
		say(false, "The provider answered, but its answer was not accepted", clip(err.Error(), 200))
		return
	}
	groups := strings.Join(auth.StripReservedGroups(claims.Groups), ", ")
	if groups == "" {
		groups = "none"
	}
	record(st.By, "ok", fmt.Sprintf("signed in at %s as %s; groups: %s", st.Issuer, claims.Username, clip(groups, 300)))
	say(true, "The test sign-in worked", "The provider signed you in as "+claims.Username+" (groups: "+groups+"). Nothing was changed on this server. Close this window and go back to the setup guide.")
}

type signinTestResult struct {
	Tested bool   `json:"tested"`
	OK     bool   `json:"ok,omitempty"`
	At     int64  `json:"at,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// GET /api/v1/signin/test: how the last test ended (the second window records it).
func (s *Server) signinTestStatus(w http.ResponseWriter, r *http.Request) {
	es, err := s.Engine.AuditLog(engine.AuditFilter{Action: testAction, User: identityFrom(r).User, Limit: 1})
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(es) == 0 {
		writeJSON(w, http.StatusOK, signinTestResult{})
		return
	}
	e := es[0]
	writeJSON(w, http.StatusOK, signinTestResult{Tested: true, OK: e.Outcome == "ok", At: e.At, Detail: e.Detail})
}
