package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/localauth"
)

// Built-in accounts: people who sign in with a password this installation
// holds (internal/localauth makes the secrets, internal/engine/accounts*.go
// keeps them). This file is the shared part: settings, the one-time setup
// that must finish before anything else works, and sign-in with its second
// step. local_enroll.go makes accounts (setup, invitations, resets);
// local_account.go is what a person does to their own account;
// local_users.go is what an administrator does to others.

// LocalAuth is the built-in accounts' settings.
type LocalAuth struct {
	// Required: built-in accounts are the installation's sign-in (mode
	// local), so nothing works until the first administrator exists.
	Required bool
	// SecondFactor is who must have one: "admins", "all" or "off".
	SecondFactor string
	// SetupCodeFile holds the one-time setup code, if the chart or the
	// operator supplies one; otherwise the server makes one.
	SetupCodeFile string
	// SetupTTL is how long a setup code lives (default 24h).
	SetupTTL time.Duration
	// Site names the installation in authenticator apps.
	Site string
}

const (
	defaultSetupTTL = localauth.DefaultSetupTTL
	signInTicketTTL = 5 * time.Minute
	pendingTTL      = 15 * time.Minute
	inviteTTL       = 72 * time.Hour
	resetTTL        = 24 * time.Hour
)

type localRuntime struct {
	ipLimit  localauth.Limiter
	setupLim localauth.Limiter
	setupMu  sync.Mutex
	done     atomic.Bool // setup is complete: never checked again
}

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,38}[a-z0-9]$`)

func (s *Server) localOn() bool { return s.Auth != nil && s.Auth.Local != nil }

func (l *LocalAuth) secondFactorRequired(role string) bool {
	switch l.SecondFactor {
	case "all":
		return true
	case "off":
		return false
	}
	return role == auth.Admin.String()
}

// localInfo adds to /api/v1/info what the sign-in page needs.
func (s *Server) localInfo(out map[string]any) {
	out["auth_mode"] = "local"
	if s.Auth.OIDC != nil {
		out["auth_mode"] = "oidc"
		out["local"] = true
	}
	if s.Auth.Local.Required {
		out["setup_required"] = s.setupPending()
	}
}

// setupPending says whether this installation still waits for its first
// administrator. Once false it stays false for this process.
func (s *Server) setupPending() bool {
	if !s.localOn() || !s.Auth.Local.Required || s.local.done.Load() {
		return false
	}
	n, err := s.Engine.CountLocalAccounts()
	if err != nil {
		return true // when in doubt, stay closed
	}
	if n > 0 {
		s.local.done.Store(true)
		return false
	}
	return true
}

// refuseUntilSetup answers every guarded call with a plain instruction while
// the installation has no administrator.
func (s *Server) refuseUntilSetup(w http.ResponseWriter) bool {
	if !s.setupPending() {
		return false
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "this installation has not been set up yet",
		"code":  "setup_required",
		"next":  "Open this address in a browser and finish setup with the one-time setup code (the installation notes or `launchway users setup-code` say where to find it).",
	})
	return true
}

// EnsureSetupCode makes sure a new installation has a setup code, and returns
// the code if this call made it (so the caller can print it once), or "" if one
// already exists or setup is done. A code from SetupCodeFile is used as given.
func (s *Server) EnsureSetupCode() (string, error) {
	if !s.localOn() || !s.Auth.Local.Required {
		return "", nil
	}
	n, err := s.Engine.CountLocalAccounts()
	if err != nil || n > 0 {
		return "", err
	}
	now := s.Now()
	ttl := s.Auth.Local.SetupTTL
	if ttl <= 0 {
		ttl = defaultSetupTTL
	}
	if f := s.Auth.Local.SetupCodeFile; f != "" {
		if b, err := os.ReadFile(f); err == nil && strings.TrimSpace(string(b)) != "" {
			code := strings.TrimSpace(string(b))
			h := localauth.HashCode(code)
			st, err := s.Engine.SetupCodeState()
			if err != nil {
				return "", err
			}
			if st != nil && st.CodeHash == h {
				return "", nil // already recorded; its clock keeps running from the first start
			}
			return "", s.Engine.IssueSetupCode(h, now.UnixMilli(), now.Add(ttl).UnixMilli(), "file")
		}
	}
	st, err := s.Engine.SetupCodeState()
	if err != nil {
		return "", err
	}
	if st != nil && st.UsedAt == 0 && st.ExpiresAt > now.UnixMilli() {
		return "", nil
	}
	return s.NewSetupCode("generated")
}

// NewSetupCode issues a fresh setup code (replacing an unused one) and
// returns it. It fails once setup is complete.
func (s *Server) NewSetupCode(source string) (string, error) {
	now := s.Now()
	ttl := defaultSetupTTL
	if s.Auth != nil && s.Auth.Local != nil && s.Auth.Local.SetupTTL > 0 {
		ttl = s.Auth.Local.SetupTTL
	}
	code := localauth.NewSetupCode()
	if err := s.Engine.IssueSetupCode(localauth.HashCode(code), now.UnixMilli(), now.Add(ttl).UnixMilli(), source); err != nil {
		return "", err
	}
	return code, nil
}

// localGuard is what every built-in sign-in route checks first.
func (s *Server) localGuard(w http.ResponseWriter, r *http.Request) bool {
	if !s.localOn() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "built-in accounts are not turned on here",
			"code": "local_off", "next": "Set auth.mode: local, or auth.local.enabled: true next to an identity provider."})
		return false
	}
	if msg := checkCSRF(r); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return false
	}
	return true
}

// throttled applies the per-address limit that protects the cost of hashing.
func (s *Server) throttled(w http.ResponseWriter, r *http.Request, lim *localauth.Limiter, max int, window time.Duration) bool {
	if lim.Max == 0 {
		lim.Max, lim.Window = max, window
	}
	if ok, wait := lim.Allow(s.clientAddr(r), s.Now()); !ok {
		tooMany(w, wait)
		return true
	}
	return false
}

func tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(wait/time.Second) + 1
	w.Header().Set("Retry-After", fmt.Sprint(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": fmt.Sprintf("too many attempts; wait %d seconds and try again", secs), "code": "rate_limited"})
}

func (s *Server) audit(r *http.Request, user, action, outcome, detail string) {
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: user, Source: s.clientAddr(r), Action: action, Outcome: outcome, Detail: detail})
}

// ---- sign-in ----

type signInRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ticket is what the first step of sign-in hands to the second.
type ticket struct {
	User    string `json:"u"`
	Expires int64  `json:"e"`
}

var errBadCredentials = map[string]string{"error": "the user name or password is not right", "code": "invalid_credentials",
	"next": "Check them and try again. If you cannot remember them, ask an administrator for a reset link."}

// localSignIn is step one: user name and password.
func (s *Server) localSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq signInRequest
	if !decode(w, r, &rq) || s.throttled(w, r, &s.local.ipLimit, 30, 5*time.Minute) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(rq.Username))
	acct, err := s.Engine.LocalAccountByName(name)
	if err != nil && !errors.Is(err, engine.ErrNotFound) {
		s.fail(w, err)
		return
	}
	if acct == nil || acct.Disabled || acct.PasswordHash == "" {
		localauth.DummyVerify() // an unknown account takes as long to refuse as a known one
		s.audit(r, name, "auth.local.sign_in", "failed", "unknown, disabled or not yet set up")
		writeJSON(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	now := s.Now()
	if acct.LockedUntil > now.UnixMilli() {
		s.audit(r, name, "auth.local.sign_in", "denied", "the account is locked after repeated failures")
		tooMany(w, time.Duration(acct.LockedUntil-now.UnixMilli())*time.Millisecond)
		return
	}
	if !localauth.VerifyPassword(rq.Password, acct.PasswordHash) {
		n, _, err := s.Engine.LocalFailure(name, now, localauth.Backoff)
		if err != nil {
			s.Log.Error("counting a failed sign-in", "err", err)
		}
		s.audit(r, name, "auth.local.sign_in", "failed", fmt.Sprintf("wrong password (%d in a row)", n))
		writeJSON(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	if localauth.NeedsRehash(acct.PasswordHash) {
		if h, err := localauth.HashPassword(rq.Password); err == nil {
			_ = s.Engine.SetLocalPassword(name, h, acct.PasswordSetAt, "")
		}
	}
	if acct.TOTPConfirmed {
		sealer, err := s.loginSealer()
		if err != nil {
			s.fail(w, err)
			return
		}
		t, err := sealer.Seal(ticket{User: name, Expires: now.Add(signInTicketTTL).UnixMilli()})
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"second_factor": "totp", "ticket": t,
			"next": "Enter the six-digit code from your authenticator app, or one of your recovery codes."})
		return
	}
	if s.Auth.Local.secondFactorRequired(acct.Role) {
		s.audit(r, name, "auth.local.sign_in", "denied", "a second factor is required for this role and the account has none")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "this account must have a second factor and does not",
			"code": "second_factor_required", "next": "Ask an administrator for a reset link, or run `launchway users reset " + name + "` on the server."})
		return
	}
	s.finishLocalSignIn(w, r, acct, "password")
}

type secondFactorRequest struct {
	Ticket       string `json:"ticket"`
	Code         string `json:"code"`
	RecoveryCode string `json:"recovery_code"`
}

// localSecondFactor is step two: a TOTP code or a recovery code.
func (s *Server) localSecondFactor(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq secondFactorRequest
	if !decode(w, r, &rq) || s.throttled(w, r, &s.local.ipLimit, 30, 5*time.Minute) {
		return
	}
	sealer, err := s.loginSealer()
	if err != nil {
		s.fail(w, err)
		return
	}
	var t ticket
	now := s.Now()
	if sealer.Open(rq.Ticket, &t) != nil || now.UnixMilli() >= t.Expires {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "the sign-in took too long or did not start here", "code": "ticket_expired", "next": "Start again with your user name and password."})
		return
	}
	acct, err := s.Engine.LocalAccountByName(t.User)
	if err != nil || acct.Disabled || !acct.TOTPConfirmed {
		writeJSON(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	if acct.LockedUntil > now.UnixMilli() {
		tooMany(w, time.Duration(acct.LockedUntil-now.UnixMilli())*time.Millisecond)
		return
	}
	ok, method := false, "totp"
	switch {
	case rq.RecoveryCode != "":
		method = "recovery code"
		ok, err = s.Engine.UseRecoveryCode(acct.Username, localauth.HashCode(rq.RecoveryCode), now.UnixMilli())
	default:
		if step, good := localauth.CheckTOTP(acct.TOTPSecret, rq.Code, now.Unix(), acct.TOTPLastStep); good {
			ok, err = s.Engine.RecordTOTPStep(acct.Username, step)
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if !ok {
		n, _, _ := s.Engine.LocalFailure(acct.Username, now, localauth.Backoff)
		s.audit(r, acct.Username, "auth.local.second_factor", "failed", fmt.Sprintf("wrong %s (%d in a row)", method, n))
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "that code is not right", "code": "invalid_code",
			"next": "Check the time on your phone, wait for the next code, or use a recovery code."})
		return
	}
	s.finishLocalSignIn(w, r, acct, method)
}

// finishLocalSignIn starts the session: a new random token every time (so a
// token from before sign-in is never promoted), HttpOnly, SameSite, Secure
// over HTTPS, ending after the idle and absolute timeouts.
func (s *Server) finishLocalSignIn(w http.ResponseWriter, r *http.Request, acct *engine.LocalAccount, method string) {
	now := s.Now()
	raw := auth.RandomString(32)
	prof := engine.Profile{Name: acct.DisplayName, Email: acct.Email}
	groups := []string{auth.LocalGroup(roleOf(acct.Role))}
	if err := s.Engine.SignIn(acct.Username, groups, prof, auth.Hash(raw), now.UnixMilli(),
		now.Add(s.Auth.Lifetime).UnixMilli(), now.Add(-s.Auth.IdleTimeout).UnixMilli()); err != nil {
		s.fail(w, err)
		return
	}
	_ = s.Engine.LocalSuccess(acct.Username, now.UnixMilli())
	http.SetCookie(w, s.cookie(r, sessionCookie, raw, "/", s.Auth.Lifetime))
	w.Header().Set("Cache-Control", "no-store")
	s.audit(r, acct.Username, "auth.local.sign_in", "ok", "with "+method)
	writeJSON(w, http.StatusOK, map[string]any{"signed_in": true, "user": acct.Username, "role": acct.Role})
}

func roleOf(s string) auth.Role {
	r, err := auth.ParseRole(s)
	if err != nil {
		return auth.NoRole
	}
	return r
}
