package api

// The repository settings page's "Who can reach this repository" and its one-step credential: a
// repository-limited token of a service account made for the repository.

import (
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

func (s *Server) repoCredentialRoutes() []route {
	const r = "/api/v1/repositories/{owner}/{name}/credentials"
	return []route{
		{"GET " + r, repoAdmin, "", s.repoReach},
		{"POST " + r, repoAdmin, "token.create", s.makeRepoCredential},
	}
}

type reachPerson struct {
	Who  string `json:"who"`
	Kind string `json:"kind"` // user or group
	Role string `json:"role"`
}

type reachToken struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Owner        string   `json:"owner"`
	Access       string   `json:"access"`
	Pattern      string   `json:"pattern"`
	ExpiresAt    int64    `json:"expires_at"`
	LastUsed     int64    `json:"last_used"`
	SourceRanges []string `json:"source_ranges,omitempty"`
}

// repoReach says who and what can reach a repository: people by role (from the access bindings, matched
// on the repository's path), and the tokens, service-account tokens included, whose limits include it.
// Deploy keys and mirrors are listed by their own calls.
func (s *Server) repoReach(w http.ResponseWriter, r *http.Request) {
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	people := []reachPerson{}
	for _, b := range s.cur().Policy.BindingsOn(path) {
		p := reachPerson{Who: b.User, Kind: "user", Role: b.Role.String()}
		if b.User == "" {
			p = reachPerson{Who: strings.TrimPrefix(b.Group, auth.LocalGroupPrefix), Kind: "group", Role: b.Role.String()}
			if strings.HasPrefix(b.Group, auth.LocalGroupPrefix) {
				p.Who = "built-in " + p.Who + "s"
			}
		}
		people = append(people, p)
	}
	all, err := s.Engine.TokenList("")
	if err != nil {
		s.fail(w, err)
		return
	}
	toks := []reachToken{}
	now := s.Now().UnixMilli()
	for _, t := range all {
		if t.RepoAccess == "" || t.ExpiresAt <= now {
			continue
		}
		for _, e := range t.Repositories {
			if coversRepo([]engine.TokenRepo{e}, path) {
				toks = append(toks, reachToken{ID: t.ID, Name: t.Name, Owner: t.OwnerName, Access: t.RepoAccess, Pattern: e.Pattern,
					ExpiresAt: t.ExpiresAt, LastUsed: t.LastUsed, SourceRanges: t.SourceRanges})
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": people, "tokens": toks})
}

// makeRepoCredential makes a token for this repository alone, read or write, for a service account made for
// the purpose (one per repository and access). Like any token of a service account it comes from a signed-in
// session, or an administrator's token where the setting allows it.
func (s *Server) makeRepoCredential(w http.ResponseWriter, r *http.Request) {
	if !s.tokensOn(w) {
		return
	}
	id := identityFrom(r)
	var in struct {
		Name          string   `json:"name"`
		Access        string   `json:"access"`
		ExpiresInDays int      `json:"expires_in_days"`
		SourceRanges  []string `json:"source_ranges"`
	}
	if !decode(w, r, &in) {
		return
	}
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	setAudit(r, in.Name, "for "+path)
	byAdminToken := id.Via == viaToken && id.agent == nil && !strings.HasPrefix(id.User, engine.ServicePrincipalPrefix) && s.AdminTokenMakesServiceTokens
	if id.Via != viaSession && !byAdminToken {
		writeErr(w, http.StatusForbidden, "a repository credential is made from a signed-in session (an administrator's API token may only where auth.tokens.service_accounts_from_tokens is on)")
		return
	}
	role := auth.Viewer
	if in.Access == accessWrite {
		role = auth.Operator
	} else if in.Access != accessRead {
		writeErr(w, http.StatusBadRequest, `the access is "read" or "write"`)
		return
	}
	acct := "repo-" + strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(path) + "-" + in.Access
	if len(acct) > 40 {
		acct = acct[:40]
	}
	if _, err := s.Engine.ServiceAccount(acct); err != nil {
		a := engine.ServiceAccount{Name: acct, Description: in.Access + " credential for " + path, Role: role.String(), CreatedAt: s.Now().UnixMilli(), CreatedBy: id.User}
		if err := s.Engine.CreateServiceAccount(a); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.mintToken(w, r, id, engine.ServicePrincipal(acct), role.String(),
		tokenRequest{Name: in.Name, ExpiresInDays: in.ExpiresInDays, Repositories: []string{path}, RepoAccess: in.Access, SourceRanges: in.SourceRanges})
}
