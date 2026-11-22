package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/forks"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// Forks and template repositories (internal/forge/forks, docs/guide/forks.md,
// docs/guide/templates.md). Every route here checks, before anything is made,
// that the person may read what they copy from and make repositories where they
// want them, and none is open to a token limited to repositories: such a token
// is bound to repositories it names, and a fork or a template repository is a
// new one it does not name.

const forkBase = "/api/v1/repositories/{owner}/{name}"

func (s *Server) forkRoutes() []route {
	return []route{
		{"POST " + forkBase + "/forks", repoRead, "repository.fork", s.forkRepo},
		{"GET " + forkBase + "/forks", repoRead, "", s.listForks},
		{"GET " + forkBase + "/fork", repoRead, "", s.forkStanding},
		{"POST " + forkBase + "/sync", repoWrite, "repository.sync", s.syncFork},
		{"GET " + forkBase + "/template", repoRead, "", s.getTemplate},
		{"POST " + forkBase + "/generate", repoRead, "repository.from_template", s.useTemplate},
		{"GET /api/v1/owners/{owner}/fork-settings", administer, "", s.getOwnerForkSettings},
		{"PUT /api/v1/owners/{owner}/fork-settings", administer, "owner.fork_settings", s.setOwnerForkSettings},
	}
}

func (s *Server) forksOn(w http.ResponseWriter) bool {
	if !s.forgeOn(w) {
		return false
	}
	if s.Forge.Forks == nil {
		writeErr(w, http.StatusNotFound, "forks are not available on this server")
		return false
	}
	return true
}

// failFork turns the errors of the forks package into answers.
func (s *Server) failFork(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, forks.ErrForksOff), errors.Is(err, forks.ErrVisibility):
		writeErr(w, http.StatusForbidden, trimPrefixes(err.Error()))
	case errors.Is(err, forks.ErrRelation):
		writeErr(w, http.StatusConflict, trimPrefixes(err.Error()))
	default:
		s.failForge(w, err)
	}
}

func trimPrefixes(s string) string {
	for _, p := range []string{"forks are not allowed: ", "visibility not allowed: ", "not possible for this repository's fork relationship: "} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

// mayMakeUnder says whether a person may make repositories under an owner: their
// own space (the owner is their name) or anywhere, as an administrator.
func (s *Server) mayMakeUnder(id *identity, owner string) bool {
	return owner != "" && (owner == id.User || s.authorize(id, auth.Administer, ""))
}

// refuseLimitedToken answers 403 for a token limited to repositories.
func refuseLimitedToken(w http.ResponseWriter, id *identity) bool {
	if id.repoLimited() {
		writeErr(w, http.StatusForbidden, "this token is limited to the repositories it names, so it cannot make a repository or write to another one; use a token without that limit")
		return true
	}
	return false
}

// forkView is what a repository says about its place among forks.
type forkView struct {
	Parent     string `json:"parent,omitempty"`
	Root       string `json:"root,omitempty"`
	ForkPoint  string `json:"fork_point,omitempty"`
	ForkedBy   string `json:"forked_by,omitempty"`
	ForkedAt   int64  `json:"forked_at,omitempty"`
	ParentGone bool   `json:"parent_deleted,omitempty"`
	// Forks is how many live forks this repository has.
	Forks      int  `json:"forks"`
	AllowForks bool `json:"allow_forks"`
	// AllowForksSetting is the repository's own switch: "on", "off" or "default".
	AllowForksSetting string `json:"allow_forks_setting"`
	AllowForksWhy     string `json:"allow_forks_why"`
	Template          bool   `json:"template"`
	// Standing is how far this fork's default branch is from its parent's.
	Standing *forks.Standing `json:"standing,omitempty"`
}

func switchWord(v int) string {
	switch v {
	case 0:
		return "off"
	case 1:
		return "on"
	}
	return "default"
}

func (s *Server) forkViewOf(r *http.Request, repo forge.Repository, standing bool) (forkView, error) {
	ctx := r.Context()
	info, err := s.Forge.Forks.Info(ctx, repo.ID)
	if err != nil {
		return forkView{}, err
	}
	v := forkView{ForkPoint: info.ForkPoint, ForkedBy: info.ForkedBy, ForkedAt: info.ForkedAt, Template: info.IsTemplate, AllowForksSetting: switchWord(info.AllowForks)}
	v.AllowForks, v.AllowForksWhy, err = s.Forge.Forks.Switch(ctx, repo)
	if err != nil {
		return v, err
	}
	if list, err := s.Forge.Forks.Forks(ctx, repo.ID, 500); err == nil {
		for _, f := range list {
			if s.authorize(identityFrom(r), auth.Read, f.Path) {
				v.Forks++
			}
		}
	}
	if info.ParentID != "" {
		if parent, err := s.Forge.RepoByID(ctx, info.ParentID); err == nil && !strings.HasPrefix(parent.Path, ".trash/") {
			if s.authorize(identityFrom(r), auth.Read, parent.Path) {
				v.Parent = parent.Path
			} else {
				v.Parent = "(a repository you cannot see)"
			}
		} else {
			v.ParentGone = true
		}
		if root, err := s.Forge.RepoByID(ctx, info.RootID); err == nil && !strings.HasPrefix(root.Path, ".trash/") && s.authorize(identityFrom(r), auth.Read, root.Path) {
			v.Root = root.Path
		}
		if standing && !v.ParentGone {
			if st, err := s.Forge.Forks.Standing(ctx, repo, "", ""); err == nil {
				v.Standing = &st
			}
		}
	}
	return v, nil
}

// repoWithForks is a repository as the API shows it when forks are on.
type repoWithForks struct {
	forge.Repository
	Fork *forkView `json:"fork,omitempty"`
}

type forkBody struct {
	Owner      string           `json:"owner"`
	Name       string           `json:"name"`
	Visibility forge.Visibility `json:"visibility"`
}

func (s *Server) forkRepo(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	id := identityFrom(r)
	if refuseLimitedToken(w, id) {
		return
	}
	parent, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in forkBody
	if !decode(w, r, &in) {
		return
	}
	if in.Owner == "" {
		in.Owner = id.User
	}
	if !s.mayMakeUnder(id, in.Owner) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("you can fork into your own space (%s) or, as an administrator, into any owner", id.User))
		return
	}
	setAudit(r, parent.Path, "fork to "+repoSubject(in.Owner, orName(in.Name, parent.Name)))
	child, err := s.Forge.Forks.Fork(r.Context(), parent, forks.NewFork{Owner: in.Owner, Name: in.Name, Visibility: in.Visibility, By: id.User})
	if err != nil {
		s.failFork(w, err)
		return
	}
	setAudit(r, parent.Path, fmt.Sprintf("fork %s (%s)", child.Path, child.Visibility))
	s.emitRepository(r.Context(), id.User, child, "created", nil)
	fv, _ := s.forkViewOf(r, child, false)
	writeJSON(w, http.StatusCreated, repoWithForks{Repository: child, Fork: &fv})
}

func orName(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) listForks(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	list, err := s.Forge.Forks.Forks(r.Context(), repo.ID, 500)
	if err != nil {
		s.fail(w, err)
		return
	}
	id := identityFrom(r)
	out := []map[string]any{}
	for _, f := range list {
		if !s.authorize(id, auth.Read, f.Path) || (id.repoLimited() && !coversRepoPath(id, f.Path)) {
			continue
		}
		out = append(out, map[string]any{"path": f.Path, "owner": f.Home, "visibility": f.Visibility, "forked_by": f.ForkedBy, "forked_at": f.ForkedAt, "fork_point": f.ForkPoint})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func coversRepoPath(id *identity, path string) bool { return coversRepo(id.repos, path) }

func (s *Server) forkStanding(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	fv, err := s.forkViewOf(r, repo, true)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, fv)
}

func (s *Server) syncFork(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	id := identityFrom(r)
	if refuseLimitedToken(w, id) {
		return
	}
	fork, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Branch   string `json:"branch"`
		Upstream string `json:"upstream"`
		Mode     string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	info, err := s.Forge.Forks.Info(r.Context(), fork.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if parent, err := s.Forge.Forks.Parent(r.Context(), info); err == nil && !s.authorize(id, auth.Read, parent.Path) {
		writeErr(w, http.StatusNotFound, "its parent is a repository you cannot read")
		return
	}
	if fork.Archived {
		writeErr(w, http.StatusForbidden, "this repository is archived; its branches take no changes")
		return
	}
	if rules, _ := s.Forge.Rules.Patterns(r.Context(), fork.ID); len(rules) > 0 {
		br := orName(in.Branch, fork.DefaultBranch)
		if err := refrulesCheck(rules, br, s.authorize(id, auth.AdministerProject, fork.Path)); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
	}
	res, err := s.Forge.Forks.Sync(r.Context(), fork, in.Branch, in.Upstream, in.Mode, id.User)
	if err != nil {
		s.failFork(w, err)
		return
	}
	setAudit(r, fork.Path, fmt.Sprintf("sync %s from the parent's %s: %s", res.Branch, res.Upstream, res.Status))
	if res.Moved() {
		s.afterForkSync(r, id, fork, res)
	}
	body := map[string]any{"result": res, "message": res.Describe()}
	if !res.OK() {
		writeJSON(w, http.StatusConflict, body)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	info, err := s.Forge.Forks.Info(r.Context(), repo.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := map[string]any{"template": info.IsTemplate, "manifest_path": forks.ManifestPath}
	if info.IsTemplate {
		m, err := s.Forge.Forks.Manifest(r.Context(), repo)
		if err != nil {
			s.failFork(w, err)
			return
		}
		out["variables"], out["contents"], out["names"] = m.Variables, m.Contents, m.Names
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) useTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	id := identityFrom(r)
	if refuseLimitedToken(w, id) {
		return
	}
	tpl, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Owner           string            `json:"owner"`
		Name            string            `json:"name"`
		Visibility      forge.Visibility  `json:"visibility"`
		Values          map[string]string `json:"values"`
		IncludeBranches bool              `json:"include_branches"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Owner == "" {
		in.Owner = id.User
	}
	if !s.mayMakeUnder(id, in.Owner) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("you can make a repository in your own space (%s) or, as an administrator, under any owner", id.User))
		return
	}
	setAudit(r, tpl.Path, "from template to "+repoSubject(in.Owner, in.Name))
	child, err := s.Forge.Forks.Use(r.Context(), tpl, forks.NewFromTemplate{Owner: in.Owner, Name: in.Name, Visibility: in.Visibility, Values: in.Values, IncludeBranches: in.IncludeBranches, By: id.User})
	if err != nil {
		s.failFork(w, err)
		return
	}
	setAudit(r, tpl.Path, fmt.Sprintf("new repository %s from the template (%s)", child.Path, child.Visibility))
	s.emitRepository(r.Context(), id.User, child, "created", nil)
	writeJSON(w, http.StatusCreated, child)
}

func (s *Server) getOwnerForkSettings(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	owner := r.PathValue("owner")
	allow, set, err := s.Forge.Forks.OwnerSwitch(r.Context(), owner)
	if err != nil {
		s.fail(w, err)
		return
	}
	v := "default"
	if set && allow {
		v = "on"
	} else if set {
		v = "off"
	}
	writeJSON(w, http.StatusOK, map[string]string{"owner": owner, "allow_forks": v})
}

func (s *Server) setOwnerForkSettings(w http.ResponseWriter, r *http.Request) {
	if !s.forksOn(w) {
		return
	}
	var in struct {
		AllowForks string `json:"allow_forks"`
	}
	if !decode(w, r, &in) {
		return
	}
	owner := r.PathValue("owner")
	switch in.AllowForks {
	case "on", "off", "default":
	default:
		writeErr(w, http.StatusBadRequest, `allow_forks is "on", "off" or "default"`)
		return
	}
	if err := s.Forge.Forks.SetOwnerAllowForks(r.Context(), owner, in.AllowForks == "on", in.AllowForks == "default", identityFrom(r).User); err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, owner, "allow forks "+in.AllowForks)
	writeJSON(w, http.StatusOK, map[string]string{"owner": owner, "allow_forks": in.AllowForks})
}

// writeRepo answers with a repository, and with its place among forks when
// forks are available (its ahead and behind with ?standing=true).
func (s *Server) writeRepo(w http.ResponseWriter, r *http.Request, status int, repo forge.Repository) {
	if s.Forge == nil || s.Forge.Forks == nil {
		writeJSON(w, status, repo)
		return
	}
	fv, err := s.forkViewOf(r, repo, r.URL.Query().Get("standing") == "true")
	if err != nil {
		writeJSON(w, status, repo)
		return
	}
	writeJSON(w, status, repoWithForks{Repository: repo, Fork: &fv})
}

// applyForkSettings applies the fork switch and the template mark of a
// repository update, and says what changed.
func (s *Server) applyForkSettings(r *http.Request, path string, in repoUpdate) ([]string, error) {
	if in.AllowForks == nil && in.Template == nil {
		return nil, nil
	}
	if s.Forge.Forks == nil {
		return nil, fmt.Errorf("%w: forks are not available on this server", forge.ErrInvalid)
	}
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), path)
	if err != nil {
		return nil, err
	}
	var changes []string
	if in.AllowForks != nil {
		v := map[string]int{"default": -1, "off": 0, "on": 1}
		n, ok := v[*in.AllowForks]
		if !ok {
			return nil, fmt.Errorf("%w: allow_forks is \"on\", \"off\" or \"default\"", forge.ErrInvalid)
		}
		if err := s.Forge.Forks.SetAllowForks(r.Context(), repo.ID, n); err != nil {
			return nil, err
		}
		changes = append(changes, "allow forks "+*in.AllowForks)
	}
	if in.Template != nil {
		if err := s.Forge.Forks.SetTemplate(r.Context(), repo.ID, *in.Template); err != nil {
			return nil, err
		}
		changes = append(changes, fmt.Sprintf("template %v", *in.Template))
	}
	return changes, nil
}

// afterForkSync tells pull requests and pipelines that a fork's branch moved.
func (s *Server) afterForkSync(r *http.Request, id *identity, fork forge.Repository, res forks.SyncResult) {
	if s.Forge.Pulls == nil {
		return
	}
	ups := []pulls.RefUpdate{{Name: "refs/heads/" + res.Branch, New: res.Head}}
	s.Forge.Pulls.Pushed(r.Context(), fork.ID, id.User, ups)
	s.pullRefsMoved(r.Context(), id.User, fork, ups)
}
