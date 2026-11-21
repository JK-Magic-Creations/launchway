package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/localauth"
)

// Making accounts: the first administrator (with the one-time setup code),
// and people welcomed or recovered by a one-time link. Each is two steps so
// that nothing is created until the second factor, where one is required, has
// been proven with a code: begin checks the secret and the password and
// hands back a second-factor secret to put in an authenticator app; complete
// takes the code and creates the account. What passes between the steps is
// sealed (and short-lived), never stored.

type pending struct {
	Kind     string `json:"k"` // "setup", "invite" or "reset"
	Proof    string `json:"p"` // the hash of the setup code or link
	Username string `json:"u"`
	Display  string `json:"d"`
	Email    string `json:"m"`
	Role     string `json:"r"`
	PwHash   string `json:"h"`
	Secret   string `json:"s"`
	Expires  int64  `json:"e"`
}

type beginRequest struct {
	Code        string `json:"code"`  // setup: the one-time setup code
	Token       string `json:"token"` // invite, reset: the secret in the link
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Password    string `json:"password"`
}

type completeRequest struct {
	Pending string `json:"pending"`
	Code    string `json:"code"` // a code from the authenticator app
}

var errBadSetupCode = map[string]string{"error": "the setup code is not valid", "code": "setup_code_invalid",
	"next": "Check it for typing mistakes. If it has expired (they last 24 hours) or was lost, run `launchway users setup-code` on the server for a new one."}

// setupStatus says whether setup is pending, and what the page needs.
func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"required": s.setupPending()}
	if st, err := s.Engine.SetupCodeState(); err == nil && st != nil && out["required"] == true {
		out["code_expires_at"] = st.ExpiresAt
		out["code_expired"] = st.ExpiresAt <= s.Now().UnixMilli()
	}
	if s.localOn() {
		out["second_factor"] = s.Auth.Local.SecondFactor
		out["min_password_length"] = localauth.MinPasswordLength
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setupBegin(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq beginRequest
	if !decode(w, r, &rq) || s.throttled(w, r, &s.local.setupLim, 10, 10*time.Minute) {
		return
	}
	if !s.setupPending() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "this installation has already been set up", "code": "setup_done", "next": "Sign in instead."})
		return
	}
	st, err := s.Engine.SetupCodeState()
	now := s.Now().UnixMilli()
	if err != nil {
		s.fail(w, err)
		return
	}
	if st == nil || st.UsedAt != 0 || st.ExpiresAt <= now || !localauth.SameCode(rq.Code, st.CodeHash) {
		s.audit(r, "", "auth.setup", "failed", "a wrong, used or expired setup code")
		writeJSON(w, http.StatusForbidden, errBadSetupCode)
		return
	}
	s.beginEnrollment(w, r, pending{Kind: "setup", Proof: st.CodeHash, Role: auth.Admin.String()}, rq, true)
}

func (s *Server) beginEnrollment(w http.ResponseWriter, r *http.Request, p pending, rq beginRequest, chooseName bool) {
	if chooseName {
		p.Username = strings.ToLower(strings.TrimSpace(rq.Username))
		if !usernameRe.MatchString(p.Username) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "the user name must be 3 to 40 letters, digits, dots, dashes or underscores, starting and ending with a letter or digit",
				"code": "invalid_username", "next": "Choose a name such as ada or ada.lovelace."})
			return
		}
	}
	p.Display, p.Email = strings.TrimSpace(rq.DisplayName), strings.TrimSpace(rq.Email)
	if p.Display == "" {
		p.Display = p.Username
	}
	if msg := localauth.CheckPassword(rq.Password, p.Username, p.Display, s.siteName()); msg != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": msg, "code": "weak_password", "next": "Choose a longer password; a few unrelated words work well."})
		return
	}
	var err error
	if p.PwHash, err = localauth.HashPassword(rq.Password); err != nil {
		s.fail(w, err)
		return
	}
	required := s.Auth.Local.secondFactorRequired(p.Role)
	out := map[string]any{"second_factor_required": required, "username": p.Username}
	// A second factor stores a secret, and secrets are kept sealed under the
	// root key: with none, a person cannot enroll one. Where the role must
	// have one, the account cannot be made until encryption is on (or the
	// administrator explicitly sets auth.local.second_factor to off).
	if !s.Engine.TOTPSealing() {
		if required {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "this role must have a second factor, and a second factor stores a secret, which this server cannot keep because it has no root key (encryption at rest is off)",
				"code": "second_factor_needs_encryption", "next": "Turn encryption at rest on (a fresh install has a root key), or set auth.local.second_factor to off if you accept accounts with a password only. See docs/guide/encryption-at-rest.md."})
			return
		}
		out["second_factor_available"] = false
	} else {
		if p.Secret, err = localauth.NewTOTPSecret(); err != nil {
			s.fail(w, err)
			return
		}
		out["second_factor_available"] = true
		out["totp_secret"], out["totp_uri"] = p.Secret, localauth.TOTPURI(s.siteName(), p.Username, p.Secret)
	}
	p.Expires = s.Now().Add(pendingTTL).UnixMilli()
	sealer, err := s.loginSealer(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if out["pending"], err = sealer.Seal(p); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) siteName() string {
	if s.Auth != nil && s.Auth.Local != nil && s.Auth.Local.Site != "" {
		return s.Auth.Local.Site
	}
	return "Launchway"
}

// openPending reads the sealed state, checks it has not expired and, where a
// second factor is wanted, the code.
func (s *Server) openPending(w http.ResponseWriter, r *http.Request, rq completeRequest) (*pending, bool) {
	sealer, err := s.loginSealer(r.Context())
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	var p pending
	if sealer.Open(rq.Pending, &p) != nil || s.Now().UnixMilli() >= p.Expires {
		writeJSON(w, http.StatusGone, map[string]string{"error": "this step took too long or did not start here", "code": "pending_expired", "next": "Start the step again."})
		return nil, false
	}
	return &p, true
}

// factorFor checks the TOTP code of a pending enrollment: the secret to store
// ("" when the person has none and none is required) and the recovery codes to show.
func (s *Server) factorFor(w http.ResponseWriter, p *pending, code string) (secret string, step int64, ok bool) {
	required := s.Auth.Local.secondFactorRequired(p.Role)
	if p.Secret == "" && !required {
		return "", 0, true // no second factor was offered: this server has no root key
	}
	if strings.TrimSpace(code) == "" && !required {
		return "", 0, true
	}
	st, good := localauth.CheckTOTP(p.Secret, code, s.Now().Unix(), 0)
	if !good {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "that code does not match the second-factor secret", "code": "invalid_code",
			"next": "Add the secret to your authenticator app, then type the six-digit code it shows now."})
		return "", 0, false
	}
	return p.Secret, st, true
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq completeRequest
	if !decode(w, r, &rq) || s.throttled(w, r, &s.local.setupLim, 10, 10*time.Minute) {
		return
	}
	p, ok := s.openPending(w, r, rq)
	if !ok || p.Kind != "setup" {
		if ok {
			writeErr(w, http.StatusBadRequest, "that is not a setup step")
		}
		return
	}
	secret, step, ok := s.factorFor(w, p, rq.Code)
	if !ok {
		return
	}
	now := s.Now().UnixMilli()
	acct := &engine.LocalAccount{Username: p.Username, DisplayName: p.Display, Email: p.Email, Role: p.Role, PasswordHash: p.PwHash,
		PasswordSetAt: now, TOTPSecret: secret, TOTPConfirmed: secret != "", TOTPLastStep: step, BreakGlass: true, CreatedAt: now, CreatedBy: "setup"}
	codes, hashes, err := recoveryCodes(secret != "")
	if err != nil {
		s.fail(w, err)
		return
	}
	switch err := s.Engine.CompleteSetup(p.Proof, acct, hashes, now); {
	case errors.Is(err, engine.ErrSetupCode):
		s.audit(r, p.Username, "auth.setup", "failed", "the setup code was used, replaced or expired before the step finished")
		writeJSON(w, http.StatusForbidden, errBadSetupCode)
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	s.local.done.Store(true)
	s.audit(r, p.Username, "auth.setup", "ok", "the first administrator was created")
	s.signInAfterEnroll(w, r, acct, codes)
}

// recoveryCodes makes a person's set of single-use codes and the Argon2id
// hashes that are stored in their place.
func recoveryCodes(has bool) ([]string, []string, error) {
	if !has {
		return nil, nil, nil
	}
	codes := localauth.NewRecoveryCodes(10)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		h, err := localauth.HashRecoveryCode(c)
		if err != nil {
			return nil, nil, err
		}
		hashes[i] = h
	}
	return codes, hashes, nil
}

func (s *Server) signInAfterEnroll(w http.ResponseWriter, r *http.Request, acct *engine.LocalAccount, codes []string) {
	now := s.Now()
	raw := auth.RandomString(32)
	if err := s.Engine.SignIn(acct.Username, []string{auth.LocalGroup(roleOf(acct.Role))}, engine.Profile{Name: acct.DisplayName, Email: acct.Email},
		auth.Hash(raw), now.UnixMilli(), now.Add(s.Auth.Lifetime).UnixMilli(), now.Add(-s.Auth.IdleTimeout).UnixMilli()); err != nil {
		s.fail(w, err)
		return
	}
	http.SetCookie(w, s.cookie(r, sessionCookie, raw, "/", s.Auth.Lifetime))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"signed_in": true, "user": acct.Username, "role": acct.Role, "recovery_codes": codes,
		"next": "Save the recovery codes somewhere safe; each works once if you lose your authenticator. They are not shown again."})
}

// ---- invitations and resets ----

func (s *Server) linkInfo(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq beginRequest
	if !decode(w, r, &rq) || s.throttled(w, r, &s.local.setupLim, 20, 10*time.Minute) {
		return
	}
	l, err := s.Engine.LinkByHash(localauth.HashCode(rq.Token), s.Now().UnixMilli())
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this link is not valid any more", "code": "link_invalid", "next": "Ask an administrator for a new link."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": l.Username, "kind": l.Kind, "expires_at": l.ExpiresAt, "min_password_length": localauth.MinPasswordLength})
}

func (s *Server) acceptBegin(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq beginRequest
	if !decode(w, r, &rq) || s.throttled(w, r, &s.local.setupLim, 20, 10*time.Minute) {
		return
	}
	h := localauth.HashCode(rq.Token)
	l, err := s.Engine.LinkByHash(h, s.Now().UnixMilli())
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this link is not valid any more", "code": "link_invalid", "next": "Ask an administrator for a new link."})
		return
	}
	acct, err := s.Engine.LocalAccountByName(l.Username)
	if err != nil {
		s.fail(w, err)
		return
	}
	rq.Username = acct.Username
	if rq.DisplayName == "" {
		rq.DisplayName = acct.DisplayName
	}
	s.beginEnrollment(w, r, pending{Kind: l.Kind, Proof: h, Username: acct.Username, Role: acct.Role}, rq, false)
}

func (s *Server) acceptComplete(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq completeRequest
	if !decode(w, r, &rq) || s.throttled(w, r, &s.local.setupLim, 20, 10*time.Minute) {
		return
	}
	p, ok := s.openPending(w, r, rq)
	if !ok || (p.Kind != engine.LinkInvite && p.Kind != engine.LinkReset) {
		if ok {
			writeErr(w, http.StatusBadRequest, "that is not an invitation or reset step")
		}
		return
	}
	secret, step, ok := s.factorFor(w, p, rq.Code)
	if !ok {
		return
	}
	now := s.Now().UnixMilli()
	if used, err := s.Engine.UseLink(p.Proof, now); err != nil || !used {
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this link was already used or has expired", "code": "link_invalid", "next": "Ask an administrator for a new link."})
		return
	}
	if err := s.Engine.SetLocalPassword(p.Username, p.PwHash, now, ""); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Engine.SetLocalTOTP(p.Username, secret, secret != ""); err != nil {
		s.fail(w, err)
		return
	}
	codes, hashes, err := recoveryCodes(secret != "")
	if err != nil {
		s.fail(w, err)
		return
	}
	if secret != "" {
		_, _ = s.Engine.RecordTOTPStep(p.Username, step)
		if err := s.Engine.ReplaceRecoveryCodes(p.Username, hashes); err != nil {
			s.fail(w, err)
			return
		}
	}
	_ = s.Engine.SetLocalProfile(p.Username, p.Display, p.Email)
	s.audit(r, p.Username, "auth.local."+p.Kind+"_accepted", "ok", "a one-time link was used")
	acct, err := s.Engine.LocalAccountByName(p.Username)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.signInAfterEnroll(w, r, acct, codes)
}
