package api

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"golang.org/x/oauth2"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Signing in with OpenID Connect: the authorization code flow with PKCE,
// state and nonce.
//
// What a sign-in needs to remember between /auth/login and /auth/callback
// is sealed (AES-GCM) into a short-lived cookie, with a key that every
// server reads from the shared database, so the callback may land on any
// server. Because the state is in the browser's own cookie, a callback
// carrying a code from someone else's sign-in fails.

const (
	loginCookie = "launchway_login"
	loginPath   = "/auth/"
	loginTTL    = 10 * time.Minute
)

// loginState is what the login cookie holds.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"r"`
	Expires  int64  `json:"e"`
}

// loginSealer returns the sealer for login cookies, creating the shared key
// the first time any server needs it.
func (s *Server) loginSealer() (*auth.Sealer, error) {
	s.sealerMu.Lock()
	defer s.sealerMu.Unlock()
	if s.sealer != nil {
		return s.sealer, nil
	}
	key, err := s.sharedSecret("login_state_key")
	if err != nil {
		return nil, err
	}
	sealer, err := auth.NewSealer(key, "launchway login state v1")
	if err != nil {
		return nil, err
	}
	s.sealer = sealer
	return sealer, nil
}

// login starts a sign-in. ?next= is where to go afterwards: a path on this
// server, or the start page.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusNotFound, "sign-in is off on this server (auth.mode: none)")
		return
	}
	sealer, err := s.loginSealer()
	if err != nil {
		s.fail(w, err)
		return
	}
	st := loginState{State: auth.RandomString(32), Nonce: auth.RandomString(32), Verifier: oauth2.GenerateVerifier(),
		Next: safeNext(r.URL.Query().Get("next")), Expires: s.Now().Add(loginTTL).UnixMilli()}
	target, err := s.Auth.OIDC.AuthURL(r.Context(), st.State, st.Nonce, st.Verifier)
	if err != nil {
		s.Log.Error("starting a sign-in", "err", err)
		writeErr(w, http.StatusBadGateway, "the sign-in provider cannot be reached; try again shortly")
		return
	}
	sealed, err := sealer.Seal(st)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, loginCookie, sealed, loginPath, loginTTL))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// callback finishes a sign-in: it checks the state, trades the code for an
// ID token (sending the PKCE verifier), checks the token and its nonce, and
// starts a session.
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusNotFound, "sign-in is off on this server (auth.mode: none)")
		return
	}
	now := s.Now()
	entry := &engine.AuditEntry{At: now.UnixMilli(), Source: s.clientAddr(r), Action: "auth.sign_in"}
	// Whatever happens, this sign-in attempt is over.
	http.SetCookie(w, s.cookie(r, loginCookie, "", loginPath, 0))
	w.Header().Set("Cache-Control", "no-store")
	fail := func(code, detail string) {
		entry.Outcome, entry.Detail = "failed", detail
		s.record(entry)
		http.Redirect(w, r, "/signin?error="+code, http.StatusFound)
	}

	var st loginState
	c, err := r.Cookie(loginCookie)
	if err != nil {
		fail("expired", "no sign-in was in progress in this browser")
		return
	}
	sealer, err := s.loginSealer()
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := sealer.Open(c.Value, &st); err != nil {
		fail("expired", "the sign-in cookie is not valid: "+err.Error())
		return
	}
	if now.UnixMilli() >= st.Expires {
		fail("expired", "the sign-in took longer than "+loginTTL.String())
		return
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		fail("state", "the state does not match the sign-in started in this browser")
		return
	}
	if e := q.Get("error"); e != "" {
		fail("provider", "the provider refused: "+e+" "+q.Get("error_description"))
		return
	}
	claims, err := s.Auth.OIDC.Exchange(r.Context(), q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		s.Log.Warn("a sign-in failed", "err", err)
		fail("failed", err.Error())
		return
	}
	entry.User = claims.Username
	raw := auth.RandomString(32)
	if err := s.Engine.SignIn(claims.Username, claims.Groups, engine.Profile{Name: claims.Name, Email: claims.Email}, auth.Hash(raw), now.UnixMilli(),
		now.Add(s.Auth.Lifetime).UnixMilli(), now.Add(-s.Auth.IdleTimeout).UnixMilli()); err != nil {
		s.Log.Error("recording a sign-in", "err", err)
		fail("unavailable", "the session could not be stored")
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, raw, "/", s.Auth.Lifetime))
	entry.Outcome, entry.Detail = "ok", "no groups"
	if len(claims.Groups) > 0 {
		entry.Detail = "groups: " + strings.Join(claims.Groups, ", ")
	}
	s.record(entry)
	http.Redirect(w, r, st.Next, http.StatusFound)
}

// logout ends the caller's session. It is a POST with the CSRF header, so
// another site cannot sign people out.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		writeErr(w, http.StatusNotFound, "sign-in is off on this server (auth.mode: none)")
		return
	}
	if msg := checkCSRF(r); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, "", "/", 0))
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	user, err := s.Engine.EndSession(auth.Hash(c.Value))
	if err != nil {
		s.fail(w, err)
		return
	}
	if user != "" {
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: user, Source: s.clientAddr(r),
			Action: "auth.sign_out", Outcome: "ok"})
	}
	w.WriteHeader(http.StatusNoContent)
}

// safeNext returns next if it is a path on this server, and "/" otherwise,
// so that sign-in cannot be used to send people to another site.
func safeNext(next string) string {
	if next == "" || len(next) > 2000 || next[0] != '/' || strings.HasPrefix(next, "//") ||
		strings.ContainsRune(next, '\\') || strings.HasPrefix(next, loginPath) {
		return "/"
	}
	for _, r := range next {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "/"
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	return next
}
