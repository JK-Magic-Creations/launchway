package api

// Service accounts: non-person accounts that own tokens for automation. A
// service account has no password and no sign-in. Its tokens act as
// "service:<name>" with the account's role everywhere (and whatever the
// access bindings of the configuration give that user name), limited further
// by each token's own limits. An administrator manages them; tokens are made
// for an account by an administrator's signed-in session, or by an
// administrator's token only where auth.tokens.service_accounts_from_tokens
// is on.

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,38}[a-z0-9]$`)

func (s *Server) serviceAccountRoutes() []route {
	return []route{
		{"GET /api/v1/service-accounts", administer, "", s.listServiceAccounts},
		{"POST /api/v1/service-accounts", administer, "service_account.create", s.createServiceAccount},
		{"PATCH /api/v1/service-accounts/{name}", administer, "service_account.update", s.updateServiceAccount},
		{"DELETE /api/v1/service-accounts/{name}", administer, "service_account.delete", s.deleteServiceAccount},
		{"GET /api/v1/service-accounts/{name}/tokens", administer, "", s.listServiceTokens},
		{"POST /api/v1/service-accounts/{name}/tokens", administer, "token.create", s.createServiceToken},
	}
}

// checkServiceTokenLimits is checkTokenLimits for a service account's token:
// its role may be no higher than the account's.
func (s *Server) checkServiceTokenLimits(accountRole, role string, projects []string) string {
	if role != "" {
		r, err := auth.ParseRole(role)
		have, _ := auth.ParseRole(accountRole)
		if err != nil || r == auth.NoRole {
			return "the role is viewer, operator or admin, or leave it out for the account's own"
		}
		if r > have {
			return "a token's role can be no higher than its account's: the account is " + accountRole
		}
	}
	return s.checkTokenLimits(&identity{}, "", projects)
}

func (s *Server) listServiceAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.Engine.ServiceAccounts()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service_accounts": list})
}

func (s *Server) createServiceAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Role        string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if in.Role == "" {
		in.Role = auth.Viewer.String()
	}
	role, err := auth.ParseRole(in.Role)
	if !serviceNameRe.MatchString(in.Name) || err != nil || role == auth.NoRole || len(in.Description) > 300 {
		writeErr(w, http.StatusBadRequest, "give a name of 3 to 40 lowercase letters, digits, dots, dashes or underscores, a role of viewer, operator or admin (default viewer), and a description of at most 300 characters")
		return
	}
	setAudit(r, in.Name, "role "+role.String())
	err = s.Engine.CreateServiceAccount(engine.ServiceAccount{Name: in.Name, Description: strings.TrimSpace(in.Description), Role: role.String(),
		CreatedAt: s.Now().UnixMilli(), CreatedBy: identityFrom(r).User})
	if err != nil {
		s.fail(w, err)
		return
	}
	a, _ := s.Engine.ServiceAccount(in.Name)
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) updateServiceAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	setAudit(r, name, "")
	if in.Role != nil {
		if role, err := auth.ParseRole(*in.Role); err != nil || role == auth.NoRole {
			writeErr(w, http.StatusBadRequest, "the role is viewer, operator or admin")
			return
		}
	}
	if in.Role == nil && in.Disabled == nil {
		writeErr(w, http.StatusBadRequest, "say what to change: role or disabled")
		return
	}
	if err := s.Engine.SetServiceAccount(name, in.Role, in.Disabled); err != nil {
		s.fail(w, err)
		return
	}
	d := ""
	if in.Role != nil {
		d = "role " + *in.Role
	}
	if in.Disabled != nil {
		d += map[bool]string{true: " switched off", false: " switched on"}[*in.Disabled]
	}
	setAudit(r, name, strings.TrimSpace(d))
	a, _ := s.Engine.ServiceAccount(name)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteServiceAccount(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	setAudit(r, name, "with its tokens")
	if err := s.Engine.DeleteServiceAccount(name); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listServiceTokens(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, err := s.Engine.ServiceAccount(name); err != nil {
		s.fail(w, err)
		return
	}
	list, err := s.Engine.TokenList(engine.ServicePrincipal(name))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// createServiceToken makes a token for a service account. Who may: an
// administrator's signed-in session; or an administrator's API token, only if
// auth.tokens.service_accounts_from_tokens is on. An agent's token, a token
// limited to anything, and a token of another kind never may.
func (s *Server) createServiceToken(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	id := identityFrom(r)
	name := r.PathValue("name")
	var in tokenRequest
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	setAudit(r, in.Name, "for "+name)
	switch {
	case id.Via == viaSession:
	case id.Via == viaToken && id.agent == nil && id.tokenID != "" && !strings.HasPrefix(id.User, engine.ServicePrincipalPrefix) && s.AdminTokenMakesServiceTokens:
	default:
		writeErr(w, http.StatusForbidden, "a service account's tokens are made from an administrator's signed-in session"+
			map[bool]string{true: "", false: "; an administrator's API token may make them only if auth.tokens.service_accounts_from_tokens is turned on"}[s.AdminTokenMakesServiceTokens])
		return
	}
	acct, err := s.Engine.ServiceAccount(name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if acct.Disabled {
		writeErr(w, http.StatusConflict, "the service account is switched off; switch it on first")
		return
	}
	s.mintToken(w, r, id, engine.ServicePrincipal(name), acct.Role, in)
}
