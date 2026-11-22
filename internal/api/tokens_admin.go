package api

// Limits on API tokens, and their administration.

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

const (
	maxTokenPatterns = 20
	// MaxTokenDaysLimit is the most a server's configuration may allow.
	MaxTokenDaysLimit = 3650
)

func (s *Server) tokenMaxDays() int {
	if s.cur().TokenMaxDays <= 0 {
		return maxTokenDays
	}
	return min(s.cur().TokenMaxDays, MaxTokenDaysLimit)
}

// checkTokenLimits checks the role and projects a new token is limited to:
// the role may be no higher than the highest its maker has anywhere.
func (s *Server) checkTokenLimits(id *identity, role string, projects []string) string {
	if role != "" {
		r, err := auth.ParseRole(role)
		if err != nil || r == auth.NoRole {
			return "the role is viewer, operator or admin, or leave it out for your own"
		}
		if have := s.cur().Policy.RoleAnywhere(id.User, id.Groups); r > have {
			if have == auth.NoRole {
				return "you have no role on any Flight, so a token of yours can have none"
			}
			return fmt.Sprintf("a token's role can be no higher than yours: you are at most %s", have)
		}
	}
	if len(projects) > maxTokenPatterns {
		return fmt.Sprintf("at most %d Flight patterns", maxTokenPatterns)
	}
	for _, p := range projects {
		if _, err := path.Match(p, ""); err != nil || p == "" || len(p) > 100 || strings.ContainsAny(p, "/\x00") {
			return fmt.Sprintf("%q is not a Flight pattern; write a Flight's name, or a pattern such as pay-*", clip(p, 40))
		}
	}
	return ""
}

func tokenLimitsDetail(role string, projects []string) string {
	d := ""
	if role != "" {
		d += ", role " + role
	}
	if len(projects) > 0 {
		d += ", projects " + strings.Join(projects, " ")
	}
	return d
}

// tokenLimits says what a new token may be: how long it may last, and the
// highest role it may have.
func (s *Server) tokenLimits(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	id := identityFrom(r)
	role := ""
	if have := s.cur().Policy.RoleAnywhere(id.User, id.Groups); have > auth.NoRole {
		role = have.String()
	}
	writeJSON(w, http.StatusOK, map[string]any{"max_days": s.tokenMaxDays(), "default_days": min(defaultTokenDays, s.tokenMaxDays()), "your_role": role})
}

// listAllTokens lists everyone's tokens, for admins.
func (s *Server) listAllTokens(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	list, err := s.Engine.TokenList("")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// revokeAnyToken revokes anyone's token, for admins.
func (s *Server) revokeAnyToken(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	t, err := s.Engine.RevokeAnyToken(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, t.Name, "a token of "+t.User)
	w.WriteHeader(http.StatusNoContent)
}
