package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// What was said on a pull request: the conversation, reviews, threads,
// suggestions, requests for review; and the repository's merge settings and
// rules.

type threadView struct {
	ID         string          `json:"id"`
	Path       string          `json:"path"`
	Line       int             `json:"line"`
	StartLine  int             `json:"start_line,omitempty"`
	Side       string          `json:"side"`
	CommitSHA  string          `json:"commit_sha"`
	Outdated   bool            `json:"outdated"`
	Resolved   bool            `json:"resolved"`
	ResolvedBy string          `json:"resolved_by,omitempty"`
	Comments   []pulls.Comment `json:"comments"`
}

func (s *Server) pullConversation(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	cs, err := s.Forge.Pulls.Store.Comments(ctx, p.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	rs, err := s.Forge.Pulls.Store.Reviews(ctx, p.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	for i := range rs {
		rs[i].Stale = rs[i].CommitSHA != p.HeadSHA
	}
	revs, err := s.Forge.Pulls.Store.Revisions(ctx, p.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	conv, threads, byThread := []pulls.Comment{}, []threadView{}, map[string]int{}
	for _, c := range cs {
		if c.Path == "" {
			conv = append(conv, c)
			continue
		}
		i, seen := byThread[c.ThreadID]
		if !seen {
			byThread[c.ThreadID] = len(threads)
			threads = append(threads, threadView{ID: c.ThreadID, Path: c.Path, Line: c.Line, StartLine: c.StartLine, Side: c.Side, CommitSHA: c.CommitSHA,
				Outdated: c.Outdated, Resolved: c.Resolved, ResolvedBy: c.ResolvedBy})
			i = len(threads) - 1
		}
		threads[i].Comments = append(threads[i].Comments, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": conv, "threads": threads, "reviews": rs, "revisions": revs, "repository": repo.Path})
}

type commentBody struct {
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line"`
	Side      string `json:"side"`
	Commit    string `json:"commit"`
	ReplyTo   string `json:"reply_to"`
}

func (b commentBody) comment() pulls.NewComment {
	return pulls.NewComment{Body: b.Body, Path: b.Path, Line: b.Line, StartLine: b.StartLine, Side: b.Side, Commit: b.Commit, ReplyTo: b.ReplyTo}
}

func (s *Server) pullComment(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in commentBody
	if !decode(w, r, &in) {
		return
	}
	c, err := s.Forge.Pulls.CommentOn(r.Context(), repo.ID, p, identityFrom(r).User, in.comment(), "")
	if err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d comment", p.Number))
	writeJSON(w, http.StatusCreated, c)
}

type commentUpdate struct {
	Body     *string `json:"body"`
	Resolved *bool   `json:"resolved"`
}

func (s *Server) pullCommentUpdate(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in commentUpdate
	if !decode(w, r, &in) {
		return
	}
	ctx, svc, id := r.Context(), s.Forge.Pulls, identityFrom(r)
	c, err := svc.Store.Comment(ctx, p.ID, r.PathValue("id"))
	if err != nil {
		s.failPull(w, err)
		return
	}
	if in.Body == nil && in.Resolved == nil {
		writeErr(w, http.StatusBadRequest, "nothing to change: send body or resolved")
		return
	}
	if in.Body != nil {
		sug, has := pulls.ParseSuggestion(*in.Body)
		if strings.TrimSpace(*in.Body) == "" {
			writeErr(w, http.StatusBadRequest, "a comment cannot be empty; delete it instead")
			return
		}
		if c, err = svc.Store.EditComment(ctx, p.ID, c.ID, id.User, *in.Body, sug, has && c.Path != "" && c.Side == "new"); err != nil {
			s.failPull(w, err)
			return
		}
	}
	if in.Resolved != nil {
		if c.Path == "" {
			writeErr(w, http.StatusBadRequest, "only a thread of review comments on a line can be resolved")
			return
		}
		if p.Author != id.User && c.Author != id.User && !s.authorize(id, auth.Operate, repo.Path) {
			writeErr(w, http.StatusForbidden, "only the author of the pull request, of the thread, or an operator may resolve it")
			return
		}
		if err := svc.ResolveThread(ctx, repo.ID, p, c.ThreadID, id.User, *in.Resolved); err != nil {
			s.failPull(w, err)
			return
		}
		c, _ = svc.Store.Comment(ctx, p.ID, c.ID)
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d comment %s", p.Number, c.ID))
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) pullCommentDelete(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	ctx, id := r.Context(), identityFrom(r)
	c, err := s.Forge.Pulls.Store.Comment(ctx, p.ID, r.PathValue("id"))
	if err != nil {
		s.failPull(w, err)
		return
	}
	if c.Author != id.User && !s.authorize(id, auth.AdministerProject, repo.Path) {
		writeErr(w, http.StatusForbidden, "only the author of a comment or an administrator may delete it")
		return
	}
	if err := s.Forge.Pulls.Store.DeleteComment(ctx, p.ID, c.ID); err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d comment %s", p.Number, c.ID))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pullApplySuggestion(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	id := identityFrom(r)
	if c, err := s.Forge.Pulls.Store.Comment(r.Context(), p.ID, r.PathValue("id")); err == nil && c.Path != "" {
		// Applying commits to the source branch: a protected one takes it only from an administrator, as a push would.
		if rules, _ := s.Forge.Rules.Patterns(r.Context(), repo.ID); len(rules) > 0 {
			if err := refrulesCheck(rules, p.SourceBranch, s.authorize(id, auth.AdministerProject, repo.Path)); err != nil {
				writeErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}
	sha, err := s.Forge.Pulls.ApplySuggestion(r.Context(), repo.ID, p, r.PathValue("id"), id.User)
	if err != nil {
		s.failPull(w, err)
		return
	}
	s.pullRefsMoved(r.Context(), id.User, repo, []pulls.RefUpdate{{Name: "refs/heads/" + p.SourceBranch, Old: p.HeadSHA, New: sha}})
	setAudit(r, repo.Path, fmt.Sprintf("#%d suggestion applied as %s", p.Number, short(sha)))
	writeJSON(w, http.StatusOK, map[string]any{"commit": sha})
}

type reviewBody struct {
	Verdict  string        `json:"verdict"`
	Body     string        `json:"body"`
	Comments []commentBody `json:"comments"`
}

func (s *Server) pullReview(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in reviewBody
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	ri := pulls.ReviewInput{Verdict: in.Verdict, Body: in.Body, Role: s.pullRole(id, repo.Path)}
	for _, c := range in.Comments {
		ri.Comments = append(ri.Comments, c.comment())
	}
	rv, err := s.Forge.Pulls.SubmitReview(r.Context(), repo.ID, p, id.User, ri)
	if err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d review %s", p.Number, rv.Verdict))
	writeJSON(w, http.StatusCreated, rv)
}

func (s *Server) pullDismiss(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Forge.Pulls.Dismiss(r.Context(), repo.ID, p, r.PathValue("id"), identityFrom(r).User, in.Reason); err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d review %s dismissed: %s", p.Number, r.PathValue("id"), in.Reason))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pullRequestReview(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Reviewers []string `json:"reviewers"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Reviewers) == 0 {
		writeErr(w, http.StatusBadRequest, "name at least one person to ask")
		return
	}
	if err := s.Forge.Pulls.RequestReview(r.Context(), repo.ID, p, identityFrom(r).User, in.Reviewers); err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d review requested from %s", p.Number, strings.Join(in.Reviewers, ", ")))
	np, _ := s.Forge.Pulls.Store.ByID(r.Context(), p.ID)
	np.Repository = repo.Path
	v, err := s.viewPull(r, repo, np)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) getPullSettings(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	set, err := s.Forge.Pulls.Store.Settings(r.Context(), repo.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) setPullSettings(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in pulls.Settings
	cur, err := s.Forge.Pulls.Store.Settings(r.Context(), repo.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	in = cur // what the request leaves out stays as it is
	if !decode(w, r, &in) {
		return
	}
	if err := s.Forge.Pulls.Store.SetSettings(r.Context(), repo.ID, in, identityFrom(r).User); err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, "merge methods "+strings.Join(in.Allowed(), ", ")+"; default "+in.DefaultMethod)
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) getMergeRules(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	rules, err := s.Forge.Pulls.Store.Rules(r.Context(), repo.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Server) setMergeRules(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Rules []pulls.Rule `json:"rules"`
	}
	if !decode(w, r, &in) {
		return
	}
	for i := range in.Rules {
		if err := in.Rules[i].Validate(); err != nil {
			s.failPull(w, err)
			return
		}
	}
	id := identityFrom(r)
	for _, rule := range in.Rules {
		if err := s.Forge.Pulls.Store.SetRule(r.Context(), repo.ID, rule, id.User); err != nil {
			s.failPull(w, err)
			return
		}
	}
	patterns := make([]string, len(in.Rules))
	for i, rule := range in.Rules {
		patterns[i] = rule.Pattern + " (" + strconv.Itoa(rule.RequiredApprovals) + " approvals, checks " + strings.Join(rule.RequiredChecks, "+") + ")"
	}
	setAudit(r, repo.Path, strings.Join(patterns, "; "))
	rules, err := s.Forge.Pulls.Store.Rules(r.Context(), repo.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}
