package api

import (
	"net/http"
	"time"
)

// An installation that runs with no sign-in on an address others can reach
// (auth.mode none with allow_anonymous, which only an explicit opt-in could
// set) keeps running exactly as before an upgrade: nothing locks its people
// out. It says so on every page (/api/v1/me and /api/v1/info carry
// sign_in_off), in the start-up log and in `launchway doctor`, and offers
// "set up sign-in now": that records the request and issues a one-time setup
// code; the servers' next start then runs with built-in accounts, and the
// first administrator is made with the code. API tokens and the runner token
// keep working throughout (they do not depend on the mode).

const signInOffNext = "Set up sign-in now: it gives you a one-time code, and when the servers next start they ask for the code and then for sign-in. Or set auth.mode to local (built-in accounts) or oidc in the configuration."

// signInOffWarning is what a page shows while there is no sign-in on a reachable address.
func (s *Server) signInOffWarning() map[string]any {
	out := map[string]any{
		"warning": "This installation has no sign-in: anyone who can reach this address can start pipelines, which run code on the runners, and approve deployments.",
		"next":    signInOffNext,
	}
	if at, ok, err := s.Engine.LocalSignInRequested(); err == nil && ok {
		out["requested_at"] = at
	}
	return out
}

// setupSignInNow is the one click behind that warning. Only a server that runs
// with no sign-in on a reachable address answers; the code is returned once.
func (s *Server) setupSignInNow(w http.ResponseWriter, r *http.Request) {
	if s.Auth != nil || !s.OpenInstall {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this installation already has sign-in, or is not open", "code": "not_open"})
		return
	}
	if msg := checkCSRF(r); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	if n, err := s.Engine.CountLocalAccounts(); err != nil || n > 0 {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": "built-in accounts already exist", "code": "conflict", "next": "Set auth.mode to local in the configuration."})
		return
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	code, err := s.NewSetupCode("signin-request")
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Engine.RequestLocalSignIn(s.Now().UnixMilli()); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, identityFrom(r).User, "auth.setup_now", "ok", "sign-in was asked for: the next start of the servers runs with built-in accounts")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_code": code,
		"next":       "Keep this code. Restart the servers (for the Helm chart: kubectl rollout restart deploy/<release>-server, or roll the Rollout), or set auth.mode: local in the configuration. They then ask for this code on the first page and make the first administrator. Until they restart, nothing changes.",
	})
}

// addSignInOff adds the warning to a response of an open installation.
func (s *Server) addSignInOff(out map[string]any) {
	if s.OpenInstall {
		out["sign_in_off"] = s.signInOffWarning()
	}
}
