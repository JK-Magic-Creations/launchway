package api

// A pipeline whose repository is hosted by this same installation needs no
// credential setup. The rule: a project's repository address is one of this
// installation's own when it is, exactly, the address the installation itself
// gives for one of its repositories: the HTTPS clone address (public URL +
// /git/owner/name.git), the same without /git, or the SSH clone address, each
// with or without ".git" and a trailing slash, and with the scheme and host
// compared without regard to case. Nothing looser counts: a similar address
// on another host is another repository.
//
// Servers then read the repository from their own storage (no network, no
// token); a Station is handed, when it leases a job, a read-only credential
// made for that repository and that run, and clones from the server it already
// talks to.

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
)

// hostedSourceTTL is how long a Station's credential for the source lasts: long
// enough to clone, not to keep.
const hostedSourceTTL = 30 * time.Minute

func normalizeAddr(a string) string {
	a = strings.TrimRight(strings.TrimSpace(a), "/")
	a = strings.TrimSuffix(a, ".git")
	if u, err := url.Parse(a); err == nil && u.Scheme != "" && u.Host != "" {
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		return u.String()
	}
	return a
}

// candidatePath is the owner/name an address would name: its last two path
// segments.
func candidatePath(addr string) string {
	p := strings.TrimSpace(addr)
	if u, err := url.Parse(p); err == nil && u.Scheme != "" && u.Host != "" {
		p = u.Path
	} else if _, after, ok := strings.Cut(p, ":"); ok {
		p = after // git@host:owner/name.git
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	segs := strings.Split(p, "/")
	if len(segs) < 2 {
		return ""
	}
	return segs[len(segs)-2] + "/" + segs[len(segs)-1]
}

// hostedRepoOf is the hosted repository an address names, if it names one.
func (s *Server) hostedRepoOf(ctx context.Context, addr string) (forge.Repository, bool) {
	if s.Forge == nil || s.Forge.Provider == nil || addr == "" {
		return forge.Repository{}, false
	}
	cand := candidatePath(addr)
	if cand == "" {
		return forge.Repository{}, false
	}
	repo, err := s.Forge.Provider.Repositories().Get(ctx, cand)
	if err != nil {
		return forge.Repository{}, false
	}
	ca := repo.Clone
	want := normalizeAddr(addr)
	forms := []string{ca.HTTPS, ca.SSH}
	if i := strings.LastIndex(ca.HTTPS, "/git/"+repo.Path); i >= 0 {
		forms = append(forms, ca.HTTPS[:i]+"/"+repo.Path+".git")
	}
	for _, f := range forms {
		if f != "" && normalizeAddr(f) == want {
			return repo, true
		}
	}
	return forge.Repository{}, false
}

// Resolve and WithCache make the server the source package's reader of its
// own repositories (source.HostedLocal).
func (s *Server) Resolve(ctx context.Context, addr string) (id, path string, ok bool) {
	if s.Forge == nil || s.Forge.Local == nil {
		return "", "", false
	}
	repo, ok := s.hostedRepoOf(ctx, addr)
	return repo.ID, repo.Path, ok
}

// WithCache runs fn on a current local copy of a hosted repository.
func (s *Server) WithCache(ctx context.Context, repo string, fn func(gitDir string) error) error {
	return s.Forge.Local.WithCache(ctx, repo, fn)
}

// sourceJobFor gives a leased job whose source is a repository hosted here a
// read-only credential for it, bound to the repository and to the run.
func (s *Server) sourceJobFor(ctx context.Context, job *engine.Job) {
	if job == nil || job.Source.Kind != "git" || s.Forge == nil || s.Forge.RunTokens.Key == nil {
		return
	}
	repo, ok := s.hostedRepoOf(ctx, job.Source.URL)
	if !ok {
		return
	}
	tok, err := s.Forge.RunTokens.MintForRun(repo.ID, job.RunID, hostedSourceTTL)
	if err != nil {
		s.Log.Error("making a credential for a run's own repository", "err", err)
		return
	}
	job.Source.Hosted, job.Source.Credential = repo.Path, tok
}
