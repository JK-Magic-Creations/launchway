package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
)

// The merge queue, updating a branch, files marked viewed, and the inbox of
// pull requests that wait on a person.

// pullQueueGet shows a pull request's place in its branch's queue, and the
// whole queue of the branch it targets.
func (s *Server) pullQueueGet(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	mine, err := s.Forge.Pulls.Store.QueueEntryOf(r.Context(), p.ID)
	if err != nil {
		s.failPull(w, err)
		return
	}
	q, err := s.Forge.Pulls.Store.Queue(r.Context(), repo.ID, p.TargetBranch)
	if err != nil {
		s.failPull(w, err)
		return
	}
	rules, _ := s.Forge.Pulls.Store.Rules(r.Context(), repo.ID)
	rule, covered := pulls.RuleFor(rules, p.TargetBranch)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": covered && rule.MergeQueue, "branch": p.TargetBranch, "entry": mine, "queue": q})
}

// pullEnqueue puts a pull request in the queue of its target branch.
func (s *Server) pullEnqueue(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Method   string `json:"method"`
		Priority int    `json:"priority"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	if in.Priority != 0 && !s.authorize(id, auth.AdministerProject, repo.Path) {
		writeErr(w, http.StatusForbidden, "only an administrator of the repository can give a pull request priority in the queue")
		return
	}
	e, err := s.Forge.Pulls.Enqueue(r.Context(), repo.ID, repo.Path, p, in.Method, id.User, s.pullRole(id, repo.Path), in.Priority)
	if err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d queued for %s by %s (%s)", p.Number, p.TargetBranch, id.User, e.State))
	writeJSON(w, http.StatusOK, e)
}

// pullDequeue takes a pull request out of the queue: its author or an operator.
func (s *Server) pullDequeue(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	id := identityFrom(r)
	if p.Author != id.User && !s.authorize(id, auth.Operate, repo.Path) {
		writeErr(w, http.StatusForbidden, "only the author of the pull request or an operator can take it out of the queue")
		return
	}
	if err := s.Forge.Pulls.Dequeue(r.Context(), repo.ID, p, id.User); err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d taken out of the queue", p.Number))
	w.WriteHeader(http.StatusNoContent)
}

// pullUpdateBranch brings the source branch up to date with the target.
func (s *Server) pullUpdateBranch(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Method string `json:"method"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	if code, msg := s.mayUpdateHead(r, id, repo, p); code != 0 {
		writeErr(w, code, msg)
		return
	}
	head, err := s.Forge.Pulls.UpdateBranch(r.Context(), repo.ID, p, in.Method, id.User)
	if err != nil {
		s.failPull(w, err)
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d branch %s updated from %s (%s)", p.Number, p.SourceBranch, p.TargetBranch, short(head)))
	writeJSON(w, http.StatusOK, map[string]any{"head": head})
}

// pullViewedGet lists the files the caller has marked viewed that have not changed since.
func (s *Server) pullViewedGet(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	files, err := s.Forge.Pulls.Viewed(r.Context(), repo.ID, p, identityFrom(r).User)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewed": files})
}

// pullViewedSet marks files viewed or not for the caller.
func (s *Server) pullViewedSet(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in struct {
		Paths  []string `json:"paths"`
		Viewed bool     `json:"viewed"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Forge.Pulls.SetViewed(r.Context(), repo.ID, p, identityFrom(r).User, in.Paths, in.Viewed); err != nil {
		s.failPull(w, err)
		return
	}
	files, err := s.Forge.Pulls.Viewed(r.Context(), repo.ID, p, identityFrom(r).User)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"viewed": files})
}

// pullInbox lists the pull requests that wait on the caller, in the
// repositories they may read.
func (s *Server) pullInbox(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	id := identityFrom(r)
	items, err := s.Forge.Pulls.Store.Inbox(r.Context(), id.User, auth.StripReservedGroups(id.Groups))
	if err != nil {
		s.failPull(w, err)
		return
	}
	out := make([]pulls.InboxItem, 0, len(items))
	for _, it := range items {
		repo, err := s.Forge.RepoByID(r.Context(), it.RepoID)
		if err != nil || !s.authorize(id, auth.Read, repo.Path) {
			continue
		}
		it.Repository = repo.Path
		out = append(out, it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// notifyPull offers a pull request's event to the notification rules.
func (s *Server) notifyPull(ctx context.Context, repoPath string, e pulls.Event) {
	if s.Engine == nil {
		return
	}
	n := engine.PullNotice{Repository: repoPath, Branch: e.Pull.TargetBranch, Link: fmt.Sprintf("/repositories/%s/pulls/%d", repoPath, e.Pull.Number)}
	who := e.Actor
	name := fmt.Sprintf("%s#%d", repoPath, e.Pull.Number)
	switch e.Action {
	case "review_requested":
		asked := strings.Trim(strings.Join([]string{e.Detail["reviewers"], prefixJoin("group ", e.Detail["groups"])}, ", "), ", ")
		n.Kind, n.Title, n.Detail = "pull.review_requested", "Review requested from "+asked+" on "+name+": "+e.Pull.Title, "Open the pull request to review it."
	case "review_submitted":
		switch e.Detail["verdict"] {
		case "approve":
			n.Kind, n.Title = "pull.approved", who+" approved "+name+": "+e.Pull.Title
		case "request_changes":
			n.Kind, n.Title, n.Detail = "pull.changes_requested", who+" asked for changes on "+name+": "+e.Pull.Title, "Open the pull request to see what to change."
		default:
			if e.Detail["mentions"] != "" {
				n.Kind, n.Title = "pull.mentioned", who+" mentioned "+e.Detail["mentions"]+" in a review of "+name
			}
		}
	case "commented":
		if e.Detail["mentions"] != "" {
			n.Kind, n.Title = "pull.mentioned", who+" mentioned "+e.Detail["mentions"]+" on "+name+": "+e.Pull.Title
		}
	case "queue_failed":
		n.Kind, n.Title, n.Detail = "pull.queue_failed", name+" was taken out of the merge queue: "+e.Pull.Title, e.Detail["reason"]
	}
	if n.Kind == "" {
		return
	}
	n.Key = fmt.Sprintf("pull:%s:%s:%d", e.Pull.ID, e.Action, s.Now().UnixNano())
	s.Engine.NotifyPull(ctx, n)
}

func prefixJoin(prefix, csv string) string {
	if csv == "" {
		return ""
	}
	parts := strings.Split(csv, ",")
	for i := range parts {
		parts[i] = prefix + parts[i]
	}
	return strings.Join(parts, ", ")
}
