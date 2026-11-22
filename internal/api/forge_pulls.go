package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/githttp"
	"github.com/jk-magic-creations/launchway/internal/forge/pulls"
	"github.com/jk-magic-creations/launchway/internal/forge/webhooks"
)

// Pull requests of hosted repositories (internal/forge/pulls). A pull request
// is a change traveling from one branch to another with its conversation,
// reviews, checks and merge rules. What a person may do with one is computed
// here, once, as the `actions` list of the answer: the web interface, MCP and
// the command line show exactly that list, so none of them offers a button
// that does not work.

const prBase = "/api/v1/repositories/{owner}/{name}"

func (s *Server) pullRoutes() []route {
	return []route{
		{"GET " + prBase + "/pulls", repoRead, "", s.listPulls},
		{"POST " + prBase + "/pulls", repoWrite, "pull.create", s.createPull},
		{"GET " + prBase + "/pulls/{number}", repoRead, "", s.getPull},
		{"PATCH " + prBase + "/pulls/{number}", repoRead, "pull.update", s.updatePull},
		{"POST " + prBase + "/pulls/{number}/merge", repoWrite, "pull.merge", s.mergePull},
		{"POST " + prBase + "/pulls/{number}/revert", repoWrite, "pull.revert", s.revertPull},
		{"GET " + prBase + "/pulls/{number}/files", repoRead, "", s.pullFiles},
		{"GET " + prBase + "/pulls/{number}/patch", repoRead, "", s.pullPatch},
		{"GET " + prBase + "/pulls/{number}/commits", repoRead, "", s.pullCommits},
		{"GET " + prBase + "/pulls/{number}/conversation", repoRead, "", s.pullConversation},
		{"POST " + prBase + "/pulls/{number}/comments", repoRead, "pull.comment", s.pullComment},
		{"PATCH " + prBase + "/pulls/{number}/comments/{id}", repoRead, "pull.comment_update", s.pullCommentUpdate},
		{"DELETE " + prBase + "/pulls/{number}/comments/{id}", repoRead, "pull.comment_delete", s.pullCommentDelete},
		{"POST " + prBase + "/pulls/{number}/comments/{id}/apply", repoWrite, "pull.suggestion_apply", s.pullApplySuggestion},
		{"POST " + prBase + "/pulls/{number}/suggestions/apply", repoWrite, "pull.suggestion_apply", s.pullApplySuggestions},
		{"POST " + prBase + "/pulls/{number}/reviews", repoRead, "pull.review", s.pullReview},
		{"POST " + prBase + "/pulls/{number}/reviews/{id}/dismiss", repoWrite, "pull.review_dismiss", s.pullDismiss},
		{"POST " + prBase + "/pulls/{number}/review-requests", repoWrite, "pull.review_request", s.pullRequestReview},
		{"DELETE " + prBase + "/pulls/{number}/review-requests/{reviewer}", repoWrite, "pull.review_unrequest", s.pullUnrequestReview},
		{"GET " + prBase + "/pulls/{number}/queue", repoRead, "", s.pullQueueGet},
		{"POST " + prBase + "/pulls/{number}/queue", repoWrite, "pull.queue", s.pullEnqueue},
		{"DELETE " + prBase + "/pulls/{number}/queue", repoRead, "pull.dequeue", s.pullDequeue},
		{"POST " + prBase + "/pulls/{number}/update-branch", repoWrite, "pull.update_branch", s.pullUpdateBranch},
		{"GET " + prBase + "/pulls/{number}/viewed", repoRead, "", s.pullViewedGet},
		{"PUT " + prBase + "/pulls/{number}/viewed", repoRead, "", s.pullViewedSet},
		{"GET /api/v1/pulls/inbox", signedIn, "", s.pullInbox},
		{"GET " + prBase + "/pulls/{number}/owners", repoRead, "", s.pullOwners},
		{"GET " + prBase + "/pulls/{number}/checks", repoRead, "", s.pullChecks},
		{"POST " + prBase + "/owners/check", repoRead, "pull.owners_check", s.checkOwnersFile},
		{"GET " + prBase + "/compare", repoRead, "", s.compareBranches},
		{"GET " + prBase + "/pull-settings", repoRead, "", s.getPullSettings},
		{"PUT " + prBase + "/pull-settings", repoAdmin, "repository.pull_settings", s.setPullSettings},
		{"GET " + prBase + "/merge-rules", repoRead, "", s.getMergeRules},
		{"PUT " + prBase + "/merge-rules", repoAdmin, "repository.merge_rules", s.setMergeRules},
	}
}

// pullsOn is forgeOn and a check that pull requests are available.
func (s *Server) pullsOn(w http.ResponseWriter) bool {
	if !s.forgeOn(w) {
		return false
	}
	if s.Forge.Pulls == nil {
		writeErr(w, http.StatusNotFound, "pull requests are not available on this server")
		return false
	}
	return true
}

func (s *Server) failPull(w http.ResponseWriter, err error) {
	var nr *pulls.NotReadyError
	switch {
	case errors.As(err, &nr):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "not_ready", "next": "Do what the sentence says, then merge again; the pull request page lists each item.", "readiness": nr.Readiness})
	case errors.Is(err, pulls.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such pull request, comment or review")
	case errors.Is(err, pulls.ErrInvalid):
		writeErr(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "invalid pull request: "))
	case errors.Is(err, pulls.ErrConflict):
		writeErr(w, http.StatusConflict, strings.TrimPrefix(err.Error(), "pull request conflict: "))
	case errors.Is(err, pulls.ErrForbidden):
		writeErr(w, http.StatusForbidden, "only the author of a comment may change its text")
	default:
		s.fail(w, err)
	}
}

// pullRole is the caller's role on a repository, as the pull request rules count it.
func (s *Server) pullRole(id *identity, path string) string {
	switch {
	case s.authorize(id, auth.AdministerProject, path):
		return pulls.RoleAdmin
	case s.authorize(id, auth.Operate, path):
		return pulls.RoleOperator
	case s.authorize(id, auth.Read, path):
		return pulls.RoleViewer
	}
	return pulls.RoleNone
}

// pullTarget is the repository and the pull request a request names.
func (s *Server) pullTarget(w http.ResponseWriter, r *http.Request) (forge.Repository, pulls.PullRequest, bool) {
	if !s.pullsOn(w) {
		return forge.Repository{}, pulls.PullRequest{}, false
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return repo, pulls.PullRequest{}, false
	}
	n, err := strconv.Atoi(r.PathValue("number"))
	if err != nil || n < 1 {
		writeErr(w, http.StatusBadRequest, "a pull request is named by its number, such as 12")
		return repo, pulls.PullRequest{}, false
	}
	p, err := s.Forge.Pulls.Store.Get(r.Context(), repo.ID, n)
	if err != nil {
		s.failPull(w, err)
		return repo, p, false
	}
	p.Repository = repo.Path
	return repo, p, true
}

func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	q, me := r.URL.Query(), identityFrom(r).User
	who := func(v string) string {
		if v == "@me" {
			return me
		}
		return v
	}
	f := pulls.Filter{State: q.Get("state"), Author: who(q.Get("author")), Assignee: who(q.Get("assignee")), Reviewer: who(q.Get("reviewer")),
		Label: q.Get("label"), Source: q.Get("source"), Target: q.Get("target"), Query: q.Get("q")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	if f.State == "" {
		f.State = "open"
	}
	items, total, err := s.Forge.Pulls.Store.List(r.Context(), repo.ID, f)
	if err != nil {
		s.failPull(w, err)
		return
	}
	for i := range items {
		items[i].Repository = repo.Path
		items[i].SourceRepo = s.forkPath(r.Context(), items[i].SourceRepo)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// pullAction is the one place an action's availability is decided.
func (s *Server) pullActions(id *identity, repo forge.Repository, p pulls.PullRequest, rd pulls.Readiness, queued *pulls.QueueEntry) []engine.Action {
	op := s.authorize(id, auth.Operate, repo.Path)
	mine := p.Author == id.User && id.User != ""
	open := p.State == pulls.Open
	var acts []engine.Action
	add := func(name string, ok bool, who, reason string) *engine.Action {
		a := engine.Action{Name: name, Available: ok, Who: who}
		if !ok {
			a.Reason = reason
		}
		acts = append(acts, a)
		return &acts[len(acts)-1]
	}
	notOpen := "this pull request is " + string(p.State)
	switch {
	case !open:
		add("merge", false, "operator", notOpen)
	case !op:
		add("merge", false, "operator", "you need the operator role on "+repo.Path+" to merge")
	case rd.Queue && !rd.CanBypass:
		add("merge", false, "operator", p.TargetBranch+" merges through the merge queue; add the pull request to the queue")
	case !rd.CanMerge && !rd.CanBypass:
		add("merge", false, "operator", rd.Why)
	default:
		a := add("merge", true, "operator", "")
		if !rd.CanMerge {
			a.Risk, a.Suggestion = "This merges past the rules ("+rd.Why+"); you will be asked why and it is recorded.", "Get the review or the checks first if you can."
		}
	}
	canEdit := mine || op
	add("edit", canEdit && p.State != pulls.Merged, "author or operator", map[bool]string{true: "a merged pull request cannot be edited", false: "only the author or an operator may edit"}[canEdit])
	add("close", open && canEdit, "author or operator", map[bool]string{true: notOpen, false: "only the author or an operator may close"}[open])
	add("reopen", p.State == pulls.Closed && canEdit, "author or operator", map[string]string{"merged": "a merged pull request cannot be reopened; revert it instead", "open": "this pull request is already open"}[string(p.State)])
	acts = append(acts, queueActions(p, rd, queued, open, op, canEdit)...)
	add("mark_ready", open && p.Draft && canEdit, "author or operator", "only a draft can be marked ready")
	add("mark_draft", open && !p.Draft && canEdit, "author or operator", "this pull request is already a draft")
	add("review", s.authorize(id, auth.Read, repo.Path), "viewer", "you cannot read this repository")
	add("request_review", open && op, "operator", notOpen)
	add("auto_merge", open && op && p.AutoMerge == "", "operator", "auto-merge is already on or the pull request is not open")
	add("cancel_auto_merge", open && canEdit && p.AutoMerge != "", "author or operator", "auto-merge is not on")
	add("revert", p.State == pulls.Merged && op && p.MergeMethod != "manual", "operator", "only a pull request merged here can be reverted")
	return acts
}

type pullView struct {
	pulls.PullRequest
	Readiness pulls.Readiness `json:"readiness"`
	Actions   []engine.Action `json:"actions"`
	// FetchRef is where git can fetch the head: git fetch origin <FetchRef>.
	FetchRef string `json:"fetch_ref"`
	YourRole string `json:"your_role"`
	// Queue is the pull request's place in the merge queue (nil: never queued); a removed entry keeps its reason.
	Queue *pulls.QueueEntry `json:"queue,omitempty"`
}

func (s *Server) viewPull(r *http.Request, repo forge.Repository, p pulls.PullRequest) (pullView, error) {
	id := identityFrom(r)
	role := s.pullRole(id, repo.Path)
	rd, err := s.Forge.Pulls.Readiness(r.Context(), repo.ID, repo.Path, p, role)
	if err != nil {
		return pullView{}, err
	}
	qe, _ := s.Forge.Pulls.Store.QueueEntryOf(r.Context(), p.ID)
	v := pullView{PullRequest: p, Readiness: rd, Actions: s.pullActions(id, repo, p, rd, qe), Queue: qe, FetchRef: pulls.PullRef(p.Number), YourRole: role}
	v.SourceRepo = s.forkPath(r.Context(), p.SourceRepo) // a fork is named by its path, not its ID
	return v, nil
}

func (s *Server) getPull(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	v, err := s.viewPull(r, repo, p)
	if err != nil {
		s.failPull(w, err)
		return
	}
	if p.State == pulls.Open && (p.Mergeability.State == pulls.MergeChecking || p.Mergeability.State == pulls.MergeUnknown) {
		s.Forge.Pulls.Schedule(r.Context(), repo.ID, p.ID) // the judgment is made in the background; this answer says "checking"
	}
	writeJSON(w, http.StatusOK, v)
}

type createPullBody struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Source    string   `json:"source_branch"`
	Target    string   `json:"target_branch"`
	Draft     bool     `json:"draft"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	Reviewers []string `json:"requested_reviewers"`
}

func (s *Server) createPull(w http.ResponseWriter, r *http.Request) {
	if !s.pullsOn(w) {
		return
	}
	repo, ok := s.repoOf(w, r)
	if !ok {
		return
	}
	var in createPullBody
	if !decode(w, r, &in) {
		return
	}
	if in.Target == "" {
		in.Target = repo.DefaultBranch
	}
	if repo.Archived {
		writeErr(w, http.StatusForbidden, "this repository is archived and takes no new pull requests")
		return
	}
	p, err := s.Forge.Pulls.Open(r.Context(), repo.ID, identityFrom(r).User, pulls.New{Title: in.Title, Body: in.Body, SourceBranch: in.Source, TargetBranch: in.Target,
		Draft: in.Draft, Labels: in.Labels, Assignees: in.Assignees, Reviewers: in.Reviewers})
	if err != nil {
		s.failPull(w, err)
		return
	}
	p.Repository = repo.Path
	setAudit(r, repo.Path, fmt.Sprintf("#%d %s into %s", p.Number, p.SourceBranch, p.TargetBranch))
	v, err := s.viewPull(r, repo, p)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

type updatePullBody struct {
	Title     *string   `json:"title"`
	Body      *string   `json:"body"`
	Draft     *bool     `json:"draft"`
	Labels    *[]string `json:"labels"`
	Target    *string   `json:"target_branch"`
	Assignees *[]string `json:"assignees"`
	// State is "closed" or "open" (reopen); AutoMerge is merge, squash, rebase, or "" to turn it off.
	State     *string `json:"state"`
	AutoMerge *string `json:"auto_merge"`
}

// setPullState closes or reopens a pull request.
func setPullState(ctx context.Context, svc *pulls.Service, repoID string, p pulls.PullRequest, state, actor string) error {
	switch state {
	case "closed":
		return svc.Close(ctx, repoID, p, actor)
	case "open":
		return svc.Reopen(ctx, repoID, p, actor)
	}
	return fmt.Errorf("%w: state is closed or open", pulls.ErrInvalid)
}

func (s *Server) updatePull(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	id := identityFrom(r)
	var in updatePullBody
	if !decode(w, r, &in) {
		return
	}
	op := s.authorize(id, auth.Operate, repo.Path)
	if !op && (p.Author != id.User || id.User == "") {
		writeErr(w, http.StatusForbidden, "only the author or someone with the operator role on "+repo.Path+" may change a pull request")
		return
	}
	ctx, svc := r.Context(), s.Forge.Pulls
	var changes []string
	if in.Title != nil || in.Body != nil || in.Draft != nil || in.Labels != nil {
		np, err := svc.Edit(ctx, repo.ID, p, pulls.Edit{Title: in.Title, Body: in.Body, Draft: in.Draft, Labels: in.Labels}, id.User)
		if err != nil {
			s.failPull(w, err)
			return
		}
		p = np
		changes = append(changes, "edited")
	}
	if in.Assignees != nil {
		for _, a := range p.Assignees {
			_ = svc.Store.RemovePerson(ctx, p.ID, "assignee", a)
		}
		for _, a := range *in.Assignees {
			if err := svc.Store.AddPerson(ctx, p.ID, "assignee", a, id.User); err != nil {
				s.failPull(w, err)
				return
			}
		}
		changes = append(changes, "assignees")
	}
	if in.Target != nil && *in.Target != p.TargetBranch {
		if err := svc.Retarget(ctx, repo.ID, p, *in.Target, id.User); err != nil {
			s.failPull(w, err)
			return
		}
		changes = append(changes, "target "+*in.Target)
	}
	if in.State != nil {
		if err := setPullState(ctx, svc, repo.ID, p, *in.State, id.User); err != nil {
			s.failPull(w, err)
			return
		}
		changes = append(changes, "state "+*in.State)
	}
	if in.AutoMerge != nil && id.agent != nil {
		writeErr(w, http.StatusForbidden, "an agent does not turn on auto-merge; the autonomy policy decides what merges by itself")
		return
	}
	if in.AutoMerge != nil {
		if *in.AutoMerge != "" && !op {
			writeErr(w, http.StatusForbidden, "only an operator may turn on auto-merge")
			return
		}
		cur, _ := svc.Store.ByID(ctx, p.ID)
		if err := svc.SetAutoMerge(ctx, repo.ID, cur, *in.AutoMerge, id.User); err != nil {
			s.failPull(w, err)
			return
		}
		changes = append(changes, "auto-merge "+*in.AutoMerge)
	}
	if len(changes) == 0 {
		writeErr(w, http.StatusBadRequest, "nothing to change: send title, body, draft, labels, assignees, target_branch, state or auto_merge")
		return
	}
	setAudit(r, repo.Path, fmt.Sprintf("#%d %s", p.Number, strings.Join(changes, ", ")))
	np, err := svc.Store.Get(ctx, repo.ID, p.Number)
	if err != nil {
		s.failPull(w, err)
		return
	}
	np.Repository = repo.Path
	v, err := s.viewPull(r, repo, np)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type mergeBody struct {
	Method       string `json:"method"`
	Message      string `json:"message"`
	DeleteBranch *bool  `json:"delete_branch"`
	ExpectHead   string `json:"expect_head"`
	Bypass       string `json:"bypass_reason"`
}

func (s *Server) mergePull(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	var in mergeBody
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	res, err := s.Forge.Pulls.Merge(r.Context(), repo.ID, p, pulls.MergeInput{Method: in.Method, Message: in.Message, DeleteBranch: in.DeleteBranch, ExpectHead: in.ExpectHead,
		Bypass: in.Bypass, Actor: id.User, ActorRole: s.pullRole(id, repo.Path), RepoPath: repo.Path})
	if err != nil {
		s.failPull(w, err)
		return
	}
	detail := fmt.Sprintf("#%d by %s into %s as %s", p.Number, res.Pull.MergeMethod, p.TargetBranch, short(res.Commit))
	if len(res.Bypassed) > 0 {
		detail += "; bypassed the rules: " + strings.TrimSpace(in.Bypass)
	}
	setAudit(r, repo.Path, detail)
	s.pullRefsMoved(r.Context(), id.User, repo, res.Updates)
	res.Pull.Repository = repo.Path
	v, err := s.viewPull(r, repo, res.Pull)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pull": v, "commit": res.Commit, "branch_deleted": res.BranchDeleted, "bypassed": res.Bypassed})
}

func (s *Server) revertPull(w http.ResponseWriter, r *http.Request) {
	repo, p, ok := s.pullTarget(w, r)
	if !ok {
		return
	}
	np, err := s.Forge.Pulls.Revert(r.Context(), repo.ID, p, identityFrom(r).User)
	if err != nil {
		s.failPull(w, err)
		return
	}
	np.Repository = repo.Path
	setAudit(r, repo.Path, fmt.Sprintf("#%d reverted by new pull request #%d", p.Number, np.Number))
	v, err := s.viewPull(r, repo, np)
	if err != nil {
		s.failPull(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

// pullRefsMoved tells webhooks, mirrors and pipelines that refs moved without
// a push (a merge, a suggestion applied): the same follow-up a push gets.
func (s *Server) pullRefsMoved(ctx context.Context, user string, repo forge.Repository, ups []pulls.RefUpdate) {
	var gu []githttp.RefUpdate
	for _, u := range ups {
		gu = append(gu, githttp.RefUpdate{Name: u.Name, Old: u.Old, New: u.New})
	}
	if s.Forge.Mirrors != nil {
		s.Forge.Mirrors.Pushed(ctx, repo.ID)
	}
	s.afterPush(ctx, gitCaller{user: user}, repo, gu)
}

// pullEvent is what the pull request service tells the rest of the system.
func (s *Server) pullEvent(ctx context.Context, e pulls.Event) {
	repo, err := s.Forge.RepoByID(ctx, e.RepoID)
	if err != nil {
		return
	}
	e.Pull.Repository = repo.Path
	s.workOnPull(ctx, repo, e)
	if s.Forge != nil && s.Forge.Hooks != nil {
		ev := webhooks.EventPullRequest
		switch e.Action {
		case "review_submitted", "review_dismissed":
			ev = webhooks.EventPullRequestReview
		}
		p := webhooks.Payload{Repository: s.hookRepo(repo), Sender: webhooks.Sender{Name: e.Actor}, Action: e.Action, Number: e.Pull.Number, PullRequest: pullPayload(e.Pull), Changes: e.Detail}
		s.Forge.Hooks.Emit(ctx, repo.ID, repo.Home, ev, p)
	}
	if e.Actor == "system" { // what a request does is audited by its route; the server's own doings are audited here
		s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: e.Actor, Action: "pull." + e.Action, Target: repo.Path, Outcome: "ok", Detail: fmt.Sprintf("#%d", e.Pull.Number)})
	}
	s.notifyPull(ctx, repo.Path, e)
	s.noticePull(ctx, repo, e)
	switch e.Action {
	case "opened", "synchronize", "reopened":
		// The pull ref moved: the projects that watch the repository look at it now and run what asks for pull requests.
		s.pollWatchers(repo)
	}
}

func pullPayload(p pulls.PullRequest) map[string]any {
	return map[string]any{"number": p.Number, "title": p.Title, "state": p.State, "draft": p.Draft, "author": p.Author, "source_branch": p.SourceBranch,
		"target_branch": p.TargetBranch, "head_sha": p.HeadSHA, "base_sha": p.BaseSHA, "merge_commit": p.MergeCommit, "merged": p.State == pulls.Merged, "labels": p.Labels}
}

// queueActions are the actions of the merge queue and of updating the branch.
func queueActions(p pulls.PullRequest, rd pulls.Readiness, queued *pulls.QueueEntry, open, op, canEdit bool) []engine.Action {
	var acts []engine.Action
	add := func(name string, ok bool, who, reason string) {
		a := engine.Action{Name: name, Available: ok, Who: who}
		if !ok {
			a.Reason = reason
		}
		acts = append(acts, a)
	}
	if rd.Queue {
		in := queued != nil && queued.State != pulls.QueueRemoved
		hard := pulls.QueueBlockers(rd)
		add("queue", open && op && !in && len(hard) == 0, "operator", map[bool]string{true: "it is already in the merge queue", false: strings.Join(hard, "; ")}[in])
		add("dequeue", in && canEdit, "author or operator", "it is not in the merge queue")
	}
	add("update_branch", open && canEdit && p.Mergeability.Behind > 0, "author or operator", "the branch is already up to date")
	return acts
}
