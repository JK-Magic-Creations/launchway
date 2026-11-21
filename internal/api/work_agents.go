package api

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// Agents: registering them, their tokens, the kill switch, budgets and spend
// (people), and the calls an agent itself makes (claim, heartbeat, hand back,
// request review, report usage, review a pull request). The server's rules
// about agents are enforced in internal/work and in the merge gate
// (work_pulls.go), never in the interface that calls them.

func (s *Server) workRoutes() []route {
	const a = "/api/v1/work/agents"
	const g = "/api/v1/work/agent"
	routes := append(s.workItemRoutes(), s.workPlanRoutes()...)
	return append(routes, []route{
		{"GET " + a, signedIn, "", s.agentsList},
		{"POST " + a, signedIn, "work.agent_create", s.agentCreate},
		{"GET " + a + "/{name}", signedIn, "", s.agentGet},
		{"PATCH " + a + "/{name}", signedIn, "work.agent_update", s.agentUpdate},
		{"DELETE " + a + "/{name}", signedIn, "work.agent_delete", s.agentDelete},
		{"POST " + a + "/{name}/rotate-token", signedIn, "work.agent_rotate", s.agentRotate},
		{"GET " + a + "/{name}/actions", signedIn, "", s.agentActions},
		{"GET /api/v1/work/spend", signedIn, "", s.workSpend},
		{"POST /api/v1/work/autonomy/clear-pause", signedIn, "work.autonomy_clear_pause", s.workClearPause},
		{"GET /api/v1/work/samples", signedIn, "", s.workSamples},
		{"POST /api/v1/work/samples/{id}/complete", signedIn, "work.sample_complete", s.workSampleComplete},
		{"GET " + g + "/me", signedIn, "", s.agentMe},
		{"GET " + g + "/queue", signedIn, "", s.agentQueue},
		{"POST " + g + "/claim", signedIn, "work.agent_claim", s.agentClaim},
		{"POST " + g + "/items/{ref}/heartbeat", signedIn, "work.agent_heartbeat", s.agentHeartbeat},
		{"POST " + g + "/items/{ref}/hand-back", signedIn, "work.agent_hand_back", s.agentHandBack},
		{"POST " + g + "/items/{ref}/request-review", signedIn, "work.agent_request_review", s.agentRequestReview},
		{"POST " + g + "/items/{ref}/usage", signedIn, "work.agent_usage", s.agentUsage},
		{"POST " + g + "/pull-review", signedIn, "work.agent_pull_review", s.agentPullReview},
		{"GET /api/v1/work/pull-reviews", signedIn, "", s.workPullReviews},
		{"POST /api/v1/work/import/stories", signedIn, "work.import_stories", s.workImportStories},
		{"POST /api/v1/work/import/archive", signedIn, "work.import_archive", s.workImportArchive},
	}...)
}

var lastSweep atomic.Int64

// sweepIfDue frees the work of agents whose lease ran out, at most every
// twenty seconds, whichever request comes first.
func (s *Server) sweepIfDue(r *http.Request) {
	now := time.Now().UnixMilli()
	if last := lastSweep.Load(); now-last < 20_000 || !lastSweep.CompareAndSwap(last, now) {
		return
	}
	if _, err := s.work().SweepLeases(r.Context()); err != nil {
		s.Log.Warn("freeing work whose lease ran out", "err", err)
	}
}

func (s *Server) agentsList(w http.ResponseWriter, r *http.Request) {
	s.sweepIfDue(r)
	as, err := s.work().Agents(r.Context(), s.workCaller(r))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": as})
}

func (s *Server) agentGet(w http.ResponseWriter, r *http.Request) {
	c := s.workCaller(r)
	a, err := s.work().AgentByName(r.Context(), r.PathValue("name"))
	if err == nil && !c.Read(a.Scope) {
		err = work.ErrNotFound
	}
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) agentCreate(w http.ResponseWriter, r *http.Request) {
	var in work.NewAgent
	if !decode(w, r, &in) {
		return
	}
	a, tok, err := s.work().CreateAgent(r.Context(), s.workCaller(r), in)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, a.Name, "registered at "+a.Scope.String())
	writeJSON(w, http.StatusCreated, map[string]any{"agent": a, "token": tok, "note": "this token is shown once; store it where the agent reads its secrets"})
}

func (s *Server) agentUpdate(w http.ResponseWriter, r *http.Request) {
	var p work.AgentPatch
	if !decode(w, r, &p) {
		return
	}
	a, err := s.work().UpdateAgent(r.Context(), s.workCaller(r), r.PathValue("name"), p)
	if err != nil {
		s.failWork(w, err)
		return
	}
	detail := ""
	if p.Disabled != nil {
		detail = "switched " + map[bool]string{true: "off", false: "on"}[*p.Disabled]
	}
	setAudit(r, a.Name, detail)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) agentDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.work().DeleteAgent(r.Context(), s.workCaller(r), r.PathValue("name")); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("name"), "removed")
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

func (s *Server) agentRotate(w http.ResponseWriter, r *http.Request) {
	a, tok, err := s.work().RotateAgentToken(r.Context(), s.workCaller(r), r.PathValue("name"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, a.Name, "token rotated")
	writeJSON(w, http.StatusOK, map[string]any{"agent": a, "token": tok, "note": "this token is shown once; the old one no longer works"})
}

func (s *Server) agentActions(w http.ResponseWriter, r *http.Request) {
	c := s.workCaller(r)
	a, err := s.work().AgentByName(r.Context(), r.PathValue("name"))
	if err == nil && !c.Read(a.Scope) {
		err = work.ErrNotFound
	}
	if err != nil {
		s.failWork(w, err)
		return
	}
	acts, err := s.work().AgentActions(r.Context(), a.Name, 100)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": a.Name, "actions": acts})
}

func (s *Server) workSpend(w http.ResponseWriter, r *http.Request) {
	rows, err := s.work().Spend(r.Context(), s.workCaller(r))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "note": "what agents reported; Launchway does not meter a model itself"})
}

func (s *Server) workClearPause(w http.ResponseWriter, r *http.Request) {
	var in struct{ Scope, Reason string }
	if !decode(w, r, &in) {
		return
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.work().ClearPause(r.Context(), s.workCaller(r), sc, in.Reason); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), "autonomy resumed: "+in.Reason)
	writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
}

func (s *Server) workSamples(w http.ResponseWriter, r *http.Request) {
	ss, err := s.work().Samples(r.Context(), s.workCaller(r), r.URL.Query().Get("all") != "true")
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": ss})
}

func (s *Server) workSampleComplete(w http.ResponseWriter, r *http.Request) {
	var in struct{ Note string }
	if !decode(w, r, &in) {
		return
	}
	if err := s.work().CompleteSample(r.Context(), s.workCaller(r), r.PathValue("id"), in.Note); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("id"), in.Note)
	writeJSON(w, http.StatusOK, map[string]any{"reviewed": true})
}

// ---- what an agent calls ----

// agentCaller returns the agent behind the request, or answers that this call
// is for agents.
func (s *Server) agentCaller(w http.ResponseWriter, r *http.Request) (work.Caller, *work.Agent, bool) {
	c := s.workCaller(r)
	if c.Agent == nil {
		writeErr(w, http.StatusForbidden, "this call is for agents: authenticate with an agent's token")
		return c, nil, false
	}
	return c, c.Agent, true
}

func (s *Server) agentMe(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	full, err := s.work().AgentByName(r.Context(), a.Name)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, full)
}

func (s *Server) agentQueue(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	q, err := s.work().Queue(r.Context(), a, n)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": q})
}

func (s *Server) agentClaim(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		Item         string `json:"item"`
		LeaseSeconds int    `json:"lease_seconds"`
		Note         string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	it, err := s.work().Claim(r.Context(), a, work.ClaimOptions{Item: in.Item, Lease: time.Duration(in.LeaseSeconds) * time.Second, Note: in.Note})
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, "claimed by agent "+a.Name)
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) agentHeartbeat(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		Note         string `json:"note"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	cl, err := s.work().Heartbeat(r.Context(), a, r.PathValue("ref"), in.Note, time.Duration(in.LeaseSeconds)*time.Second)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("ref"), "heartbeat")
	writeJSON(w, http.StatusOK, cl)
}

func (s *Server) agentHandBack(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	it, err := s.work().HandBack(r.Context(), a, r.PathValue("ref"), in.Reason)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, "handed back: "+in.Reason)
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) agentRequestReview(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		Summary string `json:"summary"`
	}
	if !decode(w, r, &in) {
		return
	}
	it, err := s.work().RequestReview(r.Context(), a, r.PathValue("ref"), in.Summary)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, it.Ref, "review requested")
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) agentUsage(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	var in work.Usage
	if !decode(w, r, &in) {
		return
	}
	t, err := s.work().ReportUsage(r.Context(), a, r.PathValue("ref"), in)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("ref"), "usage reported")
	writeJSON(w, http.StatusOK, t)
}

// agentPullReview stores an agent's review of a pull request. The server
// reads the pull request itself (its files, size, checks, who wrote it and who
// worked the item); nothing about independence is taken from the caller.
func (s *Server) agentPullReview(w http.ResponseWriter, r *http.Request) {
	_, a, ok := s.agentCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		Repo    string       `json:"repo"`
		Pull    int          `json:"pull"`
		Verdict work.Verdict `json:"verdict"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.pullsOn(w) {
		return
	}
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), in.Repo)
	if err != nil {
		s.failForge(w, err)
		return
	}
	p, err := s.Forge.Pulls.Store.Get(r.Context(), repo.ID, in.Pull)
	if err != nil {
		s.failPull(w, err)
		return
	}
	ch, authors, items, err := s.agentChangeOf(r.Context(), repo, p)
	if err != nil {
		s.failPull(w, err)
		return
	}
	rv, err := s.work().SubmitAgentReview(r.Context(), a, work.ReviewInput{Change: ch, Repo: repo.Path, Pull: strconv.Itoa(in.Pull), AuthorAgents: authors, ItemIDs: items, Verdict: in.Verdict})
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, repo.Path, "agent review of #"+strconv.Itoa(in.Pull)+": "+in.Verdict.Decision)
	writeJSON(w, http.StatusCreated, rv)
}

func (s *Server) workPullReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), q.Get("repo"))
	if err != nil {
		s.failForge(w, err)
		return
	}
	if c := s.workCaller(r); !c.Read(work.Scope{Kind: work.ScopeProject, ID: repo.Path}) {
		writeErr(w, http.StatusNotFound, "no such repository")
		return
	}
	rs, err := s.work().AgentReviews(r.Context(), repo.Path, q.Get("pull"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": rs})
}

// ---- imports ----

func (s *Server) workImportStories(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope   string          `json:"scope"`
		Items   []work.StoryDoc `json:"items"`
		Preview bool            `json:"preview"`
		Update  bool            `json:"update"`
	}
	if !decode(w, r, &in) {
		return
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := s.work().ImportStories(r.Context(), s.workCaller(r), sc, in.Items, in.Preview, in.Update)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), "stories: "+strconv.Itoa(rep.Created)+" new, "+strconv.Itoa(rep.Existing)+" known"+map[bool]string{true: " (preview)", false: ""}[in.Preview])
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) workImportArchive(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Repository string `json:"repository"`
		Preview    bool   `json:"preview"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !s.forgeOn(w) {
		return
	}
	repo, err := s.Forge.Provider.Repositories().Get(r.Context(), in.Repository)
	if err != nil {
		s.failForge(w, err)
		return
	}
	rep, err := s.work().PromoteArchive(r.Context(), s.workCaller(r), work.Scope{Kind: work.ScopeProject, ID: repo.Path}, repo.ID, repo.Path, in.Preview)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, repo.Path, "archive promoted: "+strconv.Itoa(rep.Created)+" items"+map[bool]string{true: " (preview)", false: ""}[in.Preview])
	writeJSON(w, http.StatusOK, rep)
}
