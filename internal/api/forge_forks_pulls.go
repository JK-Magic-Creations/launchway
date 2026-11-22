package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/forks"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// Pull requests from forks (docs/guide/forks.md). The head lives in the fork;
// what the pull request service copies into the target is that one branch, under
// the pull request's own ref (internal/forge/pulls/forkpulls.go).

func (s *Server) forkPullRoutes() []route {
	return []route{
		// Opening one needs only read access to the target and a fork you may take a
		// branch from, so it is not a repoWrite route like opening one within a repository.
		{"POST " + prBase + "/pulls/from-fork", repoRead, "pull.create", s.createForkPull},
		{"PUT " + prBase + "/pulls/{number}/maintainer-edits", repoRead, "pull.maintainer_edits", s.setMaintainerEdits},
		{"POST " + prBase + "/pulls/{number}/approve-run", repoWrite, "pull.approve_run", s.approveForkRun},
	}
}

// forkPath is the path of a fork a pull request has its head in, for showing.
func (s *Server) forkPath(ctx context.Context, id string) string {
	if id == "" || s.Forge == nil || s.Forge.RepoByID == nil {
		return id
	}
	r, err := s.Forge.RepoByID(ctx, id)
	if err != nil {
		return id
	}
	if strings.HasPrefix(r.Path, ".trash/") {
		return "(deleted fork)"
	}
	return r.Path
}

func (s *Server) createForkPull(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) || !s.forksOn(w) {
		return
	}
	id := identityFrom(r)
	if refuseLimitedToken(w, id) {
		return
	}
	base, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Head                 string   `json:"head"` // owner/name:branch, or just owner:branch for a fork of the same name
		Title                string   `json:"title"`
		Body                 string   `json:"body"`
		Target               string   `json:"target_branch"`
		Draft                bool     `json:"draft"`
		Labels               []string `json:"labels"`
		AllowMaintainerEdits bool     `json:"allow_maintainer_edits"`
	}
	if !decode(w, r, &in) {
		return
	}
	if base.Archived {
		writeErr(w, http.StatusForbidden, "this repository is archived and takes no new pull requests")
		return
	}
	fork, branch, msg := s.parseHead(r.Context(), in.Head, base)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	if !s.authorize(id, auth.Read, fork.Path) {
		writeErr(w, http.StatusNotFound, "no such repository: "+fork.Path)
		return
	}
	// What goes into the target can be read by everyone who reads the target. If that is
	// more people than read the fork, the person must be able to write to the fork: only
	// the people who own a branch may publish it to a wider audience.
	if forks.Rank(fork.Visibility) < forks.Rank(base.Visibility) && !s.authorize(id, auth.Operate, fork.Path) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("%s is less visible than %s; you need to be able to write to %s to offer one of its branches to a more public repository", fork.Path, base.Path, fork.Path))
		return
	}
	if in.Target == "" {
		in.Target = base.DefaultBranch
	}
	p, err := s.Forge.Pulls.Open(r.Context(), base.ID, id.User, pulls.New{Title: in.Title, Body: in.Body, SourceRepo: fork.ID, SourceBranch: branch,
		TargetBranch: in.Target, Draft: in.Draft, Labels: in.Labels, AllowMaintainerEdits: in.AllowMaintainerEdits})
	if err != nil {
		s.failPull(w, err)
		return
	}
	p.Repository = base.Path
	setAudit(r, base.Path, fmt.Sprintf("#%d %s:%s into %s", p.Number, fork.Path, p.SourceBranch, p.TargetBranch))
	v, err := s.viewPull(r, base, p)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// parseHead reads "owner:branch" or "owner/name:branch" into the fork and the
// branch. A bare owner names the owner's repository of the target's name.
func (s *Server) parseHead(ctx context.Context, head string, base forge.Repository) (forge.Repository, string, string) {
	head = strings.TrimSpace(head)
	spec, branch, ok := strings.Cut(head, ":")
	if !ok || spec == "" || branch == "" {
		return forge.Repository{}, "", `name the branch of the fork as "owner:branch" (or "owner/name:branch")`
	}
	path := spec
	if !strings.Contains(spec, "/") {
		path = spec + "/" + base.Name
	}
	fork, err := s.Forge.Provider.Repositories().Get(ctx, path)
	if err != nil {
		return forge.Repository{}, "", "there is no repository " + path
	}
	return fork, branch, ""
}

func (s *Server) setMaintainerEdits(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Allow bool `json:"allow"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	if p.Author != id.User && !s.authorize(id, auth.AdministerProject, repo.Path) {
		writeErr(w, http.StatusForbidden, "only the author of the pull request, or an administrator of "+repo.Path+", can change whether maintainers may edit its branch")
		return
	}
	if err := s.Forge.Pulls.SetMaintainerEdits(r.Context(), p, in.Allow); err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d maintainer edits %v", p.Number, in.Allow))
	writeJSON(w, http.StatusOK, map[string]any{"allow_maintainer_edits": in.Allow})
}

func (s *Server) approveForkRun(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	if err := s.Forge.Pulls.ApproveRun(r.Context(), p); err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d approved to run at %s", p.Number, short(p.HeadSHA)))
	writeJSON(w, http.StatusOK, map[string]any{"approved_head": p.HeadSHA})
}

// mayUpdateHead decides whether the caller may move the pull request's branch,
// for updating it and for resolving its conflicts, and says why not in words.
// A branch of the repository itself needs the operator role and honors the
// branch's protection; a branch in a fork is the fork's, so the person needs
// write access there, or the author's permission for maintainers.
func (s *Server) mayUpdateHead(r *http.Request, id *identity, repo forge.Repository, p pulls.PullRequest) (int, string) {
	ctx := r.Context()
	if p.SourceRepo == "" {
		if !s.authorize(id, auth.Operate, repo.Path) {
			return http.StatusForbidden, "you need the operator role on " + repo.Path + " to change its branches"
		}
		if rules, _ := s.Forge.Rules.Patterns(ctx, repo.ID); len(rules) > 0 {
			if err := refrulesCheck(rules, p.SourceBranch, s.authorize(id, auth.AdministerProject, repo.Path)); err != nil {
				return http.StatusForbidden, err.Error()
			}
		}
		return 0, ""
	}
	if id.repoLimited() {
		return http.StatusForbidden, "a token limited to repositories cannot change a branch of another repository"
	}
	fork, err := s.Forge.RepoByID(ctx, p.SourceRepo)
	if err != nil || strings.HasPrefix(fork.Path, ".trash/") {
		return http.StatusConflict, "the fork this pull request comes from was deleted, so its branch cannot be changed here"
	}
	if fork.Archived {
		return http.StatusForbidden, fork.Path + " is archived; its branches take no changes"
	}
	if forks.Rank(fork.Visibility) > forks.Rank(repo.Visibility) {
		return http.StatusForbidden, fmt.Sprintf("%s is more visible than %s: merging the target into its branch would copy %s's commits into it. Make %s %s first", fork.Path, repo.Path, repo.Path, fork.Path, repo.Visibility)
	}
	direct := s.authorize(id, auth.Operate, fork.Path)
	maintainer := p.AllowMaintainerEdits && s.authorize(id, auth.Operate, repo.Path)
	if !direct && !maintainer {
		if !p.AllowMaintainerEdits && s.authorize(id, auth.Operate, repo.Path) {
			return http.StatusForbidden, fmt.Sprintf("%s did not allow edits by maintainers on this pull request; ask them to turn it on, or to update the branch themselves", p.Author)
		}
		return http.StatusForbidden, "you need write access to " + fork.Path + ", or to be a maintainer of " + repo.Path + " on a pull request that allows maintainer edits"
	}
	if rules, _ := s.Forge.Rules.Patterns(ctx, fork.ID); len(rules) > 0 {
		if err := refrulesCheck(rules, p.SourceBranch, s.authorize(id, auth.AdministerProject, fork.Path)); err != nil {
			return http.StatusForbidden, "in " + fork.Path + ": " + err.Error()
		}
	}
	return 0, ""
}

// forkGate tells the project watcher whether a pull request of the project's
// repository comes from a fork, and whether its run waits for approval.
func (s *Server) forkGate(ctx context.Context, _, url string, number int, _ string) source.ForkVerdict {
	repo, ok := s.hostedRepoOf(ctx, url)
	if !ok || s.Forge == nil || s.Forge.Pulls == nil {
		return source.ForkVerdict{}
	}
	if fork, err := s.Forge.Pulls.IsFork(ctx, repo.ID, number); err != nil || !fork {
		return source.ForkVerdict{}
	}
	v := source.ForkVerdict{Fork: true, Reason: "pull request from a fork"}
	if p, err := s.Forge.Pulls.Store.Get(ctx, repo.ID, number); err == nil {
		v.Reason = fmt.Sprintf("pull request from the fork %s (%s)", s.forkPath(ctx, p.SourceRepo), p.Author)
	}
	if needs, why, err := s.Forge.Pulls.ForkRunNeedsApproval(ctx, repo.ID, number); err == nil && needs {
		v.Wait = why + "; approve it with: launchway pr approve-run " + fmt.Sprint(number)
	}
	return v
}
