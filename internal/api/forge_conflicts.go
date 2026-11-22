package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// Resolving a pull request's conflicts in the browser (internal/forge/pulls/conflicts.go).

func (s *Server) conflictRoutes() []route {
	return append(s.forkPullRoutes(), []route{
		{"GET " + prBase + "/pulls/{number}/conflicts", repoRead, "", s.pullConflicts},
		{"POST " + prBase + "/pulls/{number}/conflicts/resolve", repoRead, "pull.resolve_conflicts", s.pullResolveConflicts},
	}...)
}

func (s *Server) pullConflicts(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	cs, err := s.Forge.Pulls.Conflicts(r.Context(), repo.ID, p)
	if err != nil {
		s.failPull(w, err)
		return
	}
	// What the person may do about them, in words, so no screen offers a button that fails.
	code, why := s.mayUpdateHead(r, identityFrom(r), repo, p)
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": cs, "may_resolve": code == 0, "why_not": why, "ours": "the branch " + p.SourceBranch, "theirs": "the target " + p.TargetBranch})
}

func (s *Server) pullResolveConflicts(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectHead   string                 `json:"expect_head"`
		ExpectTarget string                 `json:"expect_target"`
		Files        []pulls.FileResolution `json:"files"`
		Method       string                 `json:"method"`
		Message      string                 `json:"message"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	if code, msg := s.mayUpdateHead(r, id, repo, p); code != 0 {
		writeErr(w, code, msg)
		return
	}
	res, err := s.Forge.Pulls.ResolveConflicts(r.Context(), repo.ID, p, pulls.ResolveInput{ExpectHead: in.ExpectHead, ExpectTarget: in.ExpectTarget,
		Files: in.Files, Method: in.Method, Message: in.Message, By: id.User})
	if err != nil {
		s.failPull(w, err)
		return
	}
	where := repo.Path
	if p.SourceRepo != "" {
		where = s.forkPath(r.Context(), p.SourceRepo)
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d conflicts resolved in %s on %s:%s (%s)", p.Number, strings.Join(res.Resolved, ", "), where, p.SourceBranch, short(res.Head)))
	writeJSON(w, http.StatusOK, res)
}

var _ = errors.New
