package api

import (
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

// The few Gitea API calls that tools which create, inspect and delete
// repositories need before they can use the Git addresses (the acceptance
// suite of the Gitea replacement, and scripts). This is the start of the
// compatibility layer of S40-03, not the layer: no issues, no pull requests,
// no webhooks. The answers carry the fields those tools read.

func (s *Server) compatRoutes() []route {
	return []route{
		{"GET /api/v1/user", signedIn, "", s.compatUser},
		{"POST /api/v1/user/repos", administer, "repository.create", s.compatCreate},
		{"GET /api/v1/repos/{owner}/{name}", repoRead, "", s.compatGet},
		{"PATCH /api/v1/repos/{owner}/{name}", repoAdmin, "repository.update", s.compatPatch},
		{"DELETE /api/v1/repos/{owner}/{name}", repoAdmin, "repository.delete", s.deleteRepo},
		{"GET /api/v1/repos/{owner}/{name}/branches/{branch}", repoRead, "", s.compatBranch},
	}
}

func (s *Server) compatUser(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"login": id.User, "username": id.User, "full_name": id.Profile.Name, "email": id.Profile.Email})
}

type compatRepo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Empty         bool   `json:"empty"`
	Mirror        bool   `json:"mirror"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
}

func (s *Server) compatShape(r *http.Request, x forge.Repository) compatRepo {
	out := compatRepo{ID: x.ID, Name: x.Name, FullName: x.Path, Private: x.Visibility != forge.Public, Archived: x.Archived, Mirror: x.Mirror,
		DefaultBranch: x.DefaultBranch, CloneURL: x.Clone.HTTPS}
	out.Owner.Login = x.Home
	if refs, err := s.Forge.Provider.Refs(r.Context(), x); err == nil {
		out.Empty = len(refs) == 0
	}
	return out
}

func (s *Server) compatGet(w http.ResponseWriter, r *http.Request) {
	got, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failForge(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.compatShape(r, got))
}

func (s *Server) compatCreate(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	var in struct {
		Name          string `json:"name"`
		Private       *bool  `json:"private"`
		DefaultBranch string `json:"default_branch"`
	}
	if !decode(w, r, &in) {
		return
	}
	vis := forge.Private
	if in.Private != nil && !*in.Private {
		vis = forge.Public
	}
	owner := identityFrom(r).User
	setAudit(r, repoSubject(owner, in.Name), "")
	got, err := s.Forge.Provider.Repositories().Create(r.Context(), forge.NewRepository{Home: owner, Name: in.Name, DefaultBranch: in.DefaultBranch, Visibility: vis})
	if err != nil {
		s.failForge(w, err)
		return
	}
	setAudit(r, got.Path, "visibility "+string(got.Visibility))
	writeJSON(w, http.StatusCreated, s.compatShape(r, got))
}

func (s *Server) compatPatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          *string `json:"name"`
		Private       *bool   `json:"private"`
		Archived      *bool   `json:"archived"`
		DefaultBranch *string `json:"default_branch"`
	}
	if !decode(w, r, &in) {
		return
	}
	// The same change as the native call; fields this server has no use for (has_actions, description) are ignored.
	u := repoUpdate{Name: in.Name, DefaultBranch: in.DefaultBranch, Archived: in.Archived}
	if in.Private != nil {
		v := forge.Private
		if !*in.Private {
			v = forge.Public
		}
		u.Visibility = &v
	}
	s.applyUpdate(w, r, u)
}

func (s *Server) compatBranch(w http.ResponseWriter, r *http.Request) {
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failForge(w, err)
		return
	}
	refs, err := s.Forge.Provider.Refs(r.Context(), repo)
	if err != nil {
		s.failForge(w, err)
		return
	}
	for _, x := range refs {
		if strings.TrimPrefix(x.Name, "refs/heads/") == r.PathValue("branch") && strings.HasPrefix(x.Name, "refs/heads/") {
			writeJSON(w, http.StatusOK, map[string]any{"name": r.PathValue("branch"), "commit": map[string]string{"id": x.Commit}})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "no such branch")
}
