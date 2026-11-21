package api

import (
	"net/http"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/localauth"
)

// What a person does to their own built-in account. Anything that changes how
// they sign in asks for the current password again (and a code if they have a
// second factor), so a left-open browser cannot take the account over.

func (s *Server) myAccount(w http.ResponseWriter, r *http.Request) (*engine.LocalAccount, bool) {
	id := identityFrom(r)
	if id.Via != viaSession || !s.localOn() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "there is no built-in account behind this sign-in", "code": "not_found",
			"next": "Built-in account settings are for people who signed in with a built-in account; a token or a provider sign-in has none."})
		return nil, false
	}
	a, err := s.Engine.LocalAccountByName(id.User)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "there is no built-in account behind this sign-in", "code": "not_found",
			"next": "Your account is managed by your identity provider."})
		return nil, false
	}
	return a, true
}

// reauth checks the password and, if the account has a second factor, a code.
func (s *Server) reauth(w http.ResponseWriter, r *http.Request, a *engine.LocalAccount, password, code string) bool {
	now := s.Now()
	if a.LockedUntil > now.UnixMilli() {
		tooMany(w, time.Duration(a.LockedUntil-now.UnixMilli())*time.Millisecond)
		return false
	}
	ok := localauth.VerifyPassword(password, a.PasswordHash)
	if ok && a.TOTPConfirmed {
		step, good := localauth.CheckTOTP(a.TOTPSecret, code, now.Unix(), a.TOTPLastStep)
		if good {
			good, _ = s.Engine.RecordTOTPStep(a.Username, step)
		}
		ok = good
	}
	if !ok {
		_, _, _ = s.Engine.LocalFailure(a.Username, now, localauth.Backoff)
		s.audit(r, a.Username, "auth.local.reauth", "failed", "the password or code was wrong")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "the password or code is not right", "code": "reauth_failed",
			"next": "Type your current password and, if you use an authenticator, the code it shows now."})
	}
	return ok
}

func (s *Server) accountView(w http.ResponseWriter, r *http.Request) {
	a, ok := s.myAccount(w, r)
	if !ok {
		return
	}
	left, _ := s.Engine.RecoveryCodesLeft(a.Username)
	writeJSON(w, http.StatusOK, map[string]any{"username": a.Username, "display_name": a.DisplayName, "email": a.Email, "role": a.Role,
		"second_factor": a.TOTPConfirmed, "second_factor_required": s.Auth.Local.secondFactorRequired(a.Role), "recovery_codes_left": left,
		"password_set_at": a.PasswordSetAt, "last_login_at": a.LastLoginAt, "break_glass": a.BreakGlass})
}

type passwordChange struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
	Code    string `json:"code"`
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	a, ok := s.myAccount(w, r)
	if !ok {
		return
	}
	var rq passwordChange
	if !decode(w, r, &rq) || !s.reauth(w, r, a, rq.Current, rq.Code) {
		return
	}
	if msg := localauth.CheckPassword(rq.New, a.Username, a.DisplayName, s.siteName()); msg != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg, "code": "weak_password", "next": "Choose a longer password; a few unrelated words work well."})
		return
	}
	h, err := localauth.HashPassword(rq.New)
	if err != nil {
		s.fail(w, err)
		return
	}
	keep := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		keep = auth.Hash(c.Value)
	}
	if err := s.Engine.SetLocalPassword(a.Username, h, s.Now().UnixMilli(), keep); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, a.Username, "auth.local.password_changed", "ok", "other sessions were ended")
	writeJSON(w, http.StatusOK, map[string]any{"changed": true, "next": "Your other signed-in browsers were signed out."})
}

type factorBegin struct {
	Password string `json:"current_password"`
}

// secondFactorBegin starts adding an authenticator: it stores an unconfirmed
// secret, which counts for nothing until a code proves it.
func (s *Server) secondFactorBegin(w http.ResponseWriter, r *http.Request) {
	a, ok := s.myAccount(w, r)
	if !ok {
		return
	}
	var rq factorBegin
	if !decode(w, r, &rq) {
		return
	}
	if a.TOTPConfirmed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a second factor is already set up", "code": "conflict", "next": "Remove it first if you want to replace it."})
		return
	}
	if !s.reauth(w, r, a, rq.Password, "") {
		return
	}
	secret, err := localauth.NewTOTPSecret()
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Engine.SetLocalTOTP(a.Username, secret, false); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"totp_secret": secret, "totp_uri": localauth.TOTPURI(s.siteName(), a.Username, secret)})
}

type factorConfirm struct {
	Code string `json:"code"`
}

func (s *Server) secondFactorConfirm(w http.ResponseWriter, r *http.Request) {
	a, ok := s.myAccount(w, r)
	if !ok {
		return
	}
	var rq factorConfirm
	if !decode(w, r, &rq) {
		return
	}
	if a.TOTPConfirmed || a.TOTPSecret == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "there is no second factor waiting to be confirmed", "code": "conflict", "next": "Start adding one first."})
		return
	}
	step, good := localauth.CheckTOTP(a.TOTPSecret, rq.Code, s.Now().Unix(), 0)
	if !good {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "that code does not match", "code": "invalid_code", "next": "Type the code your authenticator app shows now."})
		return
	}
	if err := s.Engine.SetLocalTOTP(a.Username, a.TOTPSecret, true); err != nil {
		s.fail(w, err)
		return
	}
	_, _ = s.Engine.RecordTOTPStep(a.Username, step)
	codes, hashes := recoveryCodes(true)
	if err := s.Engine.ReplaceRecoveryCodes(a.Username, hashes); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, a.Username, "auth.local.second_factor_added", "ok", "")
	writeJSON(w, http.StatusOK, map[string]any{"second_factor": true, "recovery_codes": codes,
		"next": "Save the recovery codes somewhere safe; each works once. They are not shown again."})
}

type factorRemove struct {
	Password string `json:"current_password"`
	Code     string `json:"code"`
}

func (s *Server) secondFactorRemove(w http.ResponseWriter, r *http.Request) {
	a, ok := s.myAccount(w, r)
	if !ok {
		return
	}
	var rq factorRemove
	if !decode(w, r, &rq) || !s.reauth(w, r, a, rq.Password, rq.Code) {
		return
	}
	if s.Auth.Local.secondFactorRequired(a.Role) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this installation requires a second factor for your role", "code": "second_factor_required",
			"next": "An administrator can change the requirement (auth.local.require_second_factor) if that is the intent."})
		return
	}
	if err := s.Engine.SetLocalTOTP(a.Username, "", false); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, a.Username, "auth.local.second_factor_removed", "ok", "")
	writeJSON(w, http.StatusOK, map[string]any{"second_factor": false})
}

func (s *Server) newRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	a, ok := s.myAccount(w, r)
	if !ok {
		return
	}
	var rq factorRemove
	if !decode(w, r, &rq) || !s.reauth(w, r, a, rq.Password, rq.Code) {
		return
	}
	if !a.TOTPConfirmed {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "recovery codes go with a second factor, and there is none", "code": "conflict", "next": "Set up an authenticator first."})
		return
	}
	codes, hashes := recoveryCodes(true)
	if err := s.Engine.ReplaceRecoveryCodes(a.Username, hashes); err != nil {
		s.fail(w, err)
		return
	}
	s.audit(r, a.Username, "auth.local.recovery_codes_renewed", "ok", "the old codes stopped working")
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (s *Server) signOutEverywhere(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	if id.Via != viaSession {
		writeErr(w, http.StatusBadRequest, "only a browser sign-in can be ended this way; revoke an API token from the tokens page")
		return
	}
	n, err := s.Engine.EndUserSessions(id.User)
	if err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, "", "/", 0))
	s.audit(r, id.User, "auth.sign_out_everywhere", "ok", "")
	writeJSON(w, http.StatusOK, map[string]any{"ended": n})
}
