package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
)

// The old address of a renamed or transferred repository keeps working until another repository takes it: Git over
// HTTPS and SSH is mapped to the repository where it is now (with a line in the answer that says so), the API and the
// web pages redirect to the new address. A person who may not read the repository at its new address learns nothing:
// they get the answer for an address that is not there.

// AliasService finds the repository that used to be at an address.
type AliasService interface {
	ResolveAlias(ctx context.Context, path string) (forge.Repository, bool)
}

// movedRepo is the live repository that used to be at path, if no repository is there now.
func (s *Server) movedRepo(ctx context.Context, path string) (forge.Repository, bool) {
	if s.Forge == nil || s.Forge.Aliases == nil {
		return forge.Repository{}, false
	}
	return s.Forge.Aliases.ResolveAlias(ctx, strings.TrimSuffix(path, ".git"))
}

// movedNotice is the line a Git client sees when it used an old address.
func movedNotice(old string, repo forge.Repository) string {
	return "This repository moved from " + old + " to " + repo.Path + ". The old address works for now; change your remote to " + repo.Path + " (git remote set-url origin " + repo.Clone.HTTPS + ")."
}

// redirectToMoved is the check of a non-Git repository route whose address is an old one: it answers where the
// repository is now, when the caller may read it there. ok is false when the caller may not (the answer is then the one
// for an address that is not there).
func (s *Server) redirectToMoved(id *identity, r *http.Request, moved forge.Repository) (code int, msg string, ok bool) {
	if id.repoLimited() && !id.repoIDMatches(moved) || !s.authorize(id, auth.Read, moved.Path) {
		return 0, "", false
	}
	owner, name := r.PathValue("owner"), r.PathValue("name")
	to := strings.Replace(r.URL.EscapedPath(), url.PathEscape(owner)+"/"+url.PathEscape(name), url.PathEscape(moved.Home)+"/"+url.PathEscape(moved.Name), 1)
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	return http.StatusPermanentRedirect, to, true
}

// isGitRequest says whether a request is Git over HTTP, which is mapped to the repository where it is now rather than
// redirected: Git clients do not follow a redirect on every request of a push.
func isGitRequest(r *http.Request) bool {
	return strings.HasSuffix(r.PathValue("name"), ".git") || strings.HasPrefix(r.URL.Path, "/git/")
}

// withMovedNotice puts the line about a moved repository before whatever a push is told.
func withMovedNotice(inner func(context.Context, []githttp.RefUpdate, []string) string, line string) func(context.Context, []githttp.RefUpdate, []string) string {
	return func(ctx context.Context, ups []githttp.RefUpdate, opts []string) string {
		if inner == nil {
			return line
		}
		return line + "\n" + inner(ctx, ups, opts)
	}
}

// gitFind finds the repository a Git address names: the repository there, or the one that used to be there (then
// from is the address asked for and now is where it is). A repository that is not there is found == false; err is a
// fault of the server.
func (s *Server) gitFind(ctx context.Context, path string) (repo forge.Repository, found bool, now, from string, err error) {
	repo, err = s.Forge.Provider.Repositories().Get(ctx, path)
	if err == nil {
		return repo, true, path, "", nil
	}
	if !errors.Is(err, forge.ErrNotFound) {
		return repo, false, path, "", err
	}
	if m, ok := s.movedRepo(ctx, path); ok {
		return m, true, m.Path, path, nil
	}
	return repo, false, path, "", nil
}
