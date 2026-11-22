package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/refrules"
	"github.com/jk-magic-creations/launchway/internal/forge/sshkeys"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
)

// Launchway's own Git hosting (forge.hosting; see internal/forge/hosting).
// Server.Forge is nil when hosting is off, and every route here then answers
// 404 feature_off; the data is kept.

// HostedProvider is what the API needs of the internal provider.
type HostedProvider interface {
	Repositories() forge.Repositories
	Rename(ctx context.Context, path, home, name string) (forge.Repository, error)
	SetVisibility(ctx context.Context, path string, v forge.Visibility) error
	SetArchived(ctx context.Context, path string, on bool) error
	Refs(ctx context.Context, repo forge.Repository) ([]forge.Ref, error)
}

// ForgeHosting is hosting, set up, as the API serves it.
type ForgeHosting struct {
	Provider HostedProvider
	// Git serves clone, fetch and push; RunTokens checks the read-only
	// credentials of runs. AllowInsecureHTTP lets Basic credentials travel over
	// plain HTTP to a listener that is not loopback.
	Git *githttp.Handler
	// Mirrors runs the pull and push mirrors of repositories.
	Mirrors           *mirror.Service
	RunTokens         githttp.RunTokens
	AllowInsecureHTTP bool
	// Keys holds SSH keys and deploy keys, Rules the protected branches, SSH
	// says whether and where Git over SSH is served.
	Keys  *sshkeys.Store
	Rules *refrules.Store
	SSH   SSHInfo
	// Hooks sends webhooks (nil: none).
	Hooks *webhooks.Service
	// Pulls is pull requests and code review (nil: not available); RepoByID
	// finds a repository from its ID, which the pull request service names
	// repositories by.
	Pulls    *pulls.Service
	RepoByID func(ctx context.Context, id string) (forge.Repository, error)
	// Updates reports dependencies and what the updater would open (nil: not
	// available).
	Updates UpdatesService
	// Local reads a hosted repository straight from this server's storage
	// (nil: servers fetch hosted repositories like any other).
	Local interface {
		WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error
	}
}

// The access classes of repositories. A repository's roles come from the
// same bindings as a project's, named by the repository's path
// (<owner>/<name>): a viewer reads, an operator pushes, an admin administers.
const (
	repoRead access = administer + 1 + iota
	repoWrite
	repoAdmin
)

const featureOff = "feature_off: Git hosting is off on this server (forge.hosting); turn it on to use repositories. Nothing was deleted."

func writeFeatureOff(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"error": featureOff, "code": "feature_off"})
}

// repoSubject is what a repository is called to the access policy.
func repoSubject(owner, name string) string { return owner + "/" + name }

// forgeRoutes are the routes of repositories.
func (s *Server) forgeRoutes() []route {
	return append(append(append(append(append(append(s.gitRoutes(), s.compatRoutes()...), s.mirrorRoutes()...), s.importRoutes()...), s.keyRoutes()...), s.pullRoutes()...), append(append(append(append(s.updateRoutes(), s.archiveRoutes()...), s.filesRoutes()...), s.hookRoutes()...), []route{
		{"GET /api/v1/repositories", signedIn, "", s.listRepos},
		{"POST /api/v1/repositories", administer, "repository.create", s.createRepo},
		{"GET /api/v1/repositories/{owner}/{name}", repoRead, "", s.getRepo},
		{"PATCH /api/v1/repositories/{owner}/{name}", repoAdmin, "repository.update", s.updateRepo},
		{"DELETE /api/v1/repositories/{owner}/{name}", repoAdmin, "repository.delete", s.deleteRepo},
	}...)...)
}

func (s *Server) forgeOn(w http.ResponseWriter) bool {
	if s.Forge == nil {
		writeFeatureOff(w)
		return false
	}
	return true
}

func (s *Server) failForge(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, forge.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such repository")
	case errors.Is(err, forge.ErrExists):
		writeErr(w, http.StatusConflict, "a repository with that name already exists")
	case errors.Is(err, forge.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, forge.ErrQuota):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, forge.ErrRateLimited):
		writeErr(w, http.StatusTooManyRequests, err.Error())
	default:
		s.fail(w, err)
	}
}

func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	page, err := s.Forge.Provider.Repositories().List(r.Context(), "")
	if err != nil {
		s.failForge(w, err)
		return
	}
	id := identityFrom(r)
	out := make([]forge.Repository, 0, len(page.Items))
	for _, x := range page.Items {
		if s.authorize(id, auth.Read, x.Path) && (!id.repoLimited() || id.repoIDMatches(x)) {
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, forge.Page[forge.Repository]{Items: out})
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	got, err := s.Forge.Provider.Repositories().Get(r.Context(), repoSubject(r.PathValue("owner"), r.PathValue("name")))
	if err != nil {
		s.failForge(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) createRepo(w http.ResponseWriter, r *http.Request) {
	if !s.forgeOn(w) {
		return
	}
	var in struct {
		Owner         string           `json:"owner"`
		Name          string           `json:"name"`
		DefaultBranch string           `json:"default_branch"`
		Visibility    forge.Visibility `json:"visibility"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, repoSubject(in.Owner, in.Name), "")
	got, err := s.Forge.Provider.Repositories().Create(r.Context(), forge.NewRepository{Home: in.Owner, Name: in.Name,
		DefaultBranch: in.DefaultBranch, Visibility: in.Visibility})
	if err != nil {
		s.failForge(w, err)
		return
	}
	setAudit(r, got.Path, "visibility "+string(got.Visibility))
	s.emitRepository(r.Context(), identityFrom(r).User, got, "created", nil)
	writeJSON(w, http.StatusCreated, got)
}

// repoUpdate is a change to a repository; nil fields stay as they are.
type repoUpdate struct {
	Owner         *string           `json:"owner"`
	Name          *string           `json:"name"`
	DefaultBranch *string           `json:"default_branch"`
	Visibility    *forge.Visibility `json:"visibility"`
	Archived      *bool             `json:"archived"`
}

func (s *Server) updateRepo(w http.ResponseWriter, r *http.Request) {
	var in repoUpdate
	if !decode(w, r, &in) {
		return
	}
	s.applyUpdate(w, r, in)
}

func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request, in repoUpdate) {
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	ctx, p := r.Context(), s.Forge.Provider
	var changes []string
	renamedFrom := ""
	if _, err := p.Repositories().Get(ctx, path); err != nil {
		s.failForge(w, err)
		return
	}
	if in.Visibility != nil {
		if err := p.SetVisibility(ctx, path, *in.Visibility); err != nil {
			s.failForge(w, err)
			return
		}
		changes = append(changes, "visibility "+string(*in.Visibility))
	}
	if in.DefaultBranch != nil {
		if err := p.Repositories().SetDefaultBranch(ctx, path, *in.DefaultBranch); err != nil {
			s.failForge(w, err)
			return
		}
		changes = append(changes, "default branch "+*in.DefaultBranch)
	}
	if in.Archived != nil {
		if err := p.SetArchived(ctx, path, *in.Archived); err != nil {
			s.failForge(w, err)
			return
		}
		changes = append(changes, "archived")
	}
	if in.Name != nil || in.Owner != nil {
		owner, name := r.PathValue("owner"), r.PathValue("name")
		if in.Owner != nil {
			owner = *in.Owner
		}
		if in.Name != nil {
			name = *in.Name
		}
		if _, err := p.Rename(ctx, path, owner, name); err != nil {
			s.failForge(w, err)
			return
		}
		renamedFrom = path
		path = repoSubject(owner, name)
		changes = append(changes, "renamed to "+path)
	}
	setAudit(r, path, strings.Join(changes, ", "))
	got, err := p.Repositories().Get(ctx, path)
	if err != nil {
		s.failForge(w, err)
		return
	}
	if renamedFrom != "" {
		s.tokensFollowRename(renamedFrom, got)
		s.emitRepository(ctx, identityFrom(r).User, got, "renamed", map[string]string{"old_path": renamedFrom})
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) deleteRepo(w http.ResponseWriter, r *http.Request) {
	path := repoSubject(r.PathValue("owner"), r.PathValue("name"))
	setAudit(r, path, "moved to the trash")
	gone, _ := s.Forge.Provider.Repositories().Get(r.Context(), path)
	if err := s.Forge.Provider.Repositories().Delete(r.Context(), path); err != nil {
		s.failForge(w, err)
		return
	}
	if gone.ID != "" {
		s.tokensForgetRepo(gone)
		s.emitRepository(r.Context(), identityFrom(r).User, gone, "deleted", nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkRepoRequest is checkRepo for a request whose path names the repository
// ({owner} and {name}); with hosting off it is the feature_off answer.
func (s *Server) checkRepoRequest(id *identity, a access, r *http.Request) (int, string) {
	if s.Forge == nil {
		return http.StatusNotFound, featureOff
	}
	return s.checkRepo(id, a, repoSubject(r.PathValue("owner"), r.PathValue("name")))
}

// checkRepo applies a repository access class. A repository someone may not
// read is reported exactly as one that does not exist.
func (s *Server) checkRepo(id *identity, a access, repo string) (int, string) {
	if !s.authorize(id, auth.Read, repo) {
		return http.StatusNotFound, "no such repository"
	}
	if id.repoLimited() {
		// A token limited to repositories is bound to them by ID, on every route: a repository
		// made again under a listed name, or removed some other way, is not the one it was made for.
		got, err := s.Forge.Provider.Repositories().Get(context.Background(), repo)
		if err != nil || !id.repoIDMatches(got) {
			return http.StatusNotFound, "no such repository"
		}
	}
	switch {
	case a == repoWrite && !s.authorize(id, auth.Operate, repo):
		return http.StatusForbidden, "you need the operator role on " + repo + " to push to it"
	case a == repoAdmin && !s.authorize(id, auth.AdministerProject, repo):
		return http.StatusForbidden, "you need the admin role on " + repo + " to do this"
	}
	return 0, ""
}
