package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
)

// Protected tags of a hosted repository (internal/forge/refrules/tags.go):
// the rules are read by anyone who may read the repository and replaced by its
// administrators. They are enforced in the one push policy (forge_ssh.go), so
// Git over HTTPS, Git over SSH, the tag routes and a release's tag all meet
// them.

func (s *Server) tagRuleRoutes() []route {
	const p = "/api/v1/repositories/{owner}/{name}/protected-tags"
	return []route{
		{"GET " + p, repoRead, "", s.getProtectedTags},
		{"PUT " + p, repoAdmin, "repository.protect_tags", s.setProtectedTags},
	}
}

func (s *Server) getProtectedTags(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	rules, err := s.Forge.Rules.TagRules(r.Context(), repo.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Server) setProtectedTags(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Rules []refrules.TagRule `json:"rules"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Rules == nil {
		in.Rules = []refrules.TagRule{}
	}
	if err := s.Forge.Rules.SetTagRules(r.Context(), repo.ID, in.Rules, identityFrom(r).User); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rules, err := s.Forge.Rules.TagRules(r.Context(), repo.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var parts []string
	for _, ru := range rules {
		parts = append(parts, fmt.Sprintf("%s (create: %s, move or delete: %s, %d pipelines)", ru.Pattern, ru.CreateRole, nobody(ru.ChangeRole), len(ru.Pipelines)))
	}
	setAudit(r, repo.Path, "protected tags: "+nonEmpty(strings.Join(parts, "; "), "none"))
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func nobody(role string) string {
	if role == "" {
		return "nobody"
	}
	return role
}
