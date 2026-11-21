package api

import (
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/localauth"
)

// What an administrator does to built-in accounts: list them, invite a person
// (a one-time link, since there may be no e-mail), issue a reset link for a
// person who is locked out, change a role, turn an account off, sign it out
// everywhere or delete it. The last enabled administrator is protected, and
// with an identity provider beside the built-in accounts the last
// break-glass administrator goes only after a provider sign-in has worked.

type userView struct {
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	State        string `json:"state"` // invited, active, locked or disabled
	SecondFactor bool   `json:"second_factor"`
	BreakGlass   bool   `json:"break_glass"`
	LastLoginAt  int64  `json:"last_login_at"`
	CreatedAt    int64  `json:"created_at"`
}

func (s *Server) userViewOf(a *engine.LocalAccount) userView {
	state := "active"
	switch {
	case a.Disabled:
		state = "disabled"
	case a.PasswordHash == "":
		state = "invited"
	case a.LockedUntil > s.Now().UnixMilli():
		state = "locked"
	}
	return userView{Username: a.Username, DisplayName: a.DisplayName, Email: a.Email, Role: a.Role, State: state, SecondFactor: a.TOTPConfirmed,
		BreakGlass: a.BreakGlass, LastLoginAt: a.LastLoginAt, CreatedAt: a.CreatedAt}
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if !s.localOn() {
		writeJSON(w, http.StatusOK, map[string]any{"users": []userView{}, "enabled": false})
		return
	}
	as, err := s.Engine.ListLocalAccounts()
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]userView, 0, len(as))
	for _, a := range as {
		out = append(out, s.userViewOf(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "enabled": true, "second_factor_rule": s.Auth.Local.SecondFactor})
}

type newUser struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
}

func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	var rq newUser
	if !decode(w, r, &rq) {
		return
	}
	name := strings.ToLower(strings.TrimSpace(rq.Username))
	role, err := auth.ParseRole(rq.Role)
	if !usernameRe.MatchString(name) || err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "give a user name of 3 to 40 letters, digits, dots, dashes or underscores, and a role of viewer, operator or admin", "code": "invalid"})
		return
	}
	now := s.Now().UnixMilli()
	id := identityFrom(r)
	if err := s.Engine.CreateLocalAccount(&engine.LocalAccount{Username: name, DisplayName: strings.TrimSpace(rq.DisplayName), Email: strings.TrimSpace(rq.Email),
		Role: role.String(), CreatedAt: now, CreatedBy: id.User}); err != nil {
		s.fail(w, err)
		return
	}
	s.issueLink(w, r, name, engine.LinkInvite, role.String(), inviteTTL.Milliseconds(), "invited "+name+" as "+role.String())
}

func (s *Server) issueLink(w http.ResponseWriter, r *http.Request, name, kind, role string, ttlMs int64, detail string) {
	token := localauth.NewInviteToken()
	now := s.Now().UnixMilli()
	if err := s.Engine.CreateLink(engine.Link{Hash: localauth.HashCode(token), Username: name, Kind: kind, Role: role, CreatedAt: now,
		CreatedBy: identityFrom(r).User, ExpiresAt: now + ttlMs}); err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"username": name, "path": "/accept?token=" + token, "expires_at": now + ttlMs,
		"next": "Send this link to the person by a channel you trust. It works once and is not shown again."})
}

func (s *Server) resetUser(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	name := r.PathValue("name")
	a, err := s.Engine.LocalAccountByName(name)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.issueLink(w, r, name, engine.LinkReset, a.Role, resetTTL.Milliseconds(), "reset link for "+name)
}

type userChange struct {
	Role       *string `json:"role"`
	Disabled   *bool   `json:"disabled"`
	BreakGlass *bool   `json:"break_glass"`
	Confirm    string  `json:"confirm"`
}

// lastAdminRefusal explains why an account may not stop being an enabled
// administrator, or "" if it may.
func (s *Server) lastAdminRefusal(a *engine.LocalAccount, confirm string) string {
	if a.Role != auth.Admin.String() || a.Disabled {
		return ""
	}
	n, err := s.Engine.CountLocalAdmins()
	if err != nil || n > 1 {
		return ""
	}
	if !s.Auth.Local.Required {
		// An identity provider is the sign-in: the last built-in administrator
		// (the break-glass account) goes only when someone has said so, after a
		// provider sign-in has worked at least once.
		seen, _ := s.Engine.ProviderSignInSeen()
		if !seen {
			return "this is the last built-in administrator and no one has signed in through the identity provider yet; sign in through the provider first, to prove it works, then remove this account"
		}
		if confirm != "remove-break-glass" {
			return "this is the break-glass administrator, the way back in if the provider fails; send confirm: \"remove-break-glass\" to remove it anyway"
		}
		return ""
	}
	return "this is the last administrator; make another person an administrator first"
}

func (s *Server) changeUser(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	name := r.PathValue("name")
	a, err := s.Engine.LocalAccountByName(name)
	if err != nil {
		s.fail(w, err)
		return
	}
	var rq userChange
	if !decode(w, r, &rq) {
		return
	}
	refuse := func(msg string) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "code": "last_administrator", "next": "Add or keep another administrator, then try again."})
	}
	if rq.Role != nil {
		role, err := auth.ParseRole(*rq.Role)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if role != auth.Admin {
			if msg := s.lastAdminRefusal(a, rq.Confirm); msg != "" {
				refuse(msg)
				return
			}
		}
		if s.Auth.Local.secondFactorRequired(role.String()) && !a.TOTPConfirmed {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "that role needs a second factor and " + name + " has none", "code": "second_factor_required",
				"next": "Send them a reset link (it sets one up), then change the role."})
			return
		}
		if err := s.Engine.SetLocalRole(name, role.String()); err != nil {
			s.fail(w, err)
			return
		}
		s.audit(r, identityFrom(r).User, "user.role", "ok", name+": "+a.Role+" to "+role.String())
	}
	if rq.Disabled != nil {
		if *rq.Disabled {
			if msg := s.lastAdminRefusal(a, rq.Confirm); msg != "" {
				refuse(msg)
				return
			}
		}
		if err := s.Engine.SetLocalDisabled(name, *rq.Disabled); err != nil {
			s.fail(w, err)
			return
		}
		s.audit(r, identityFrom(r).User, "user.disabled", "ok", name)
	}
	if rq.BreakGlass != nil {
		if err := s.Engine.SetLocalBreakGlass(name, *rq.BreakGlass); err != nil {
			s.fail(w, err)
			return
		}
	}
	a, _ = s.Engine.LocalAccountByName(name)
	writeJSON(w, http.StatusOK, s.userViewOf(a))
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	name := r.PathValue("name")
	a, err := s.Engine.LocalAccountByName(name)
	if err != nil {
		s.fail(w, err)
		return
	}
	var rq userChange
	if !decode(w, r, &rq) {
		return
	}
	if msg := s.lastAdminRefusal(a, rq.Confirm); msg != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "code": "last_administrator", "next": "Add or keep another administrator, then try again."})
		return
	}
	if err := s.Engine.DeleteLocalAccount(name); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) signOutUser(w http.ResponseWriter, r *http.Request) {
	if !s.localGuard(w, r) {
		return
	}
	name := r.PathValue("name")
	if _, err := s.Engine.LocalAccountByName(name); err != nil {
		s.fail(w, err)
		return
	}
	n, err := s.Engine.EndUserSessions(name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ended": n})
}
