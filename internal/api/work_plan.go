package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// Planning: boards, milestones, sprints, charts, roadmap, release notes, the
// settings of a level, the inbox and the home page's share.

func (s *Server) workPlanRoutes() []route {
	const w = "/api/v1/work"
	return []route{
		{"GET " + w + "/board", signedIn, "", s.workBoard},
		{"GET " + w + "/milestones", signedIn, "", s.workMilestones},
		{"POST " + w + "/milestones", signedIn, "work.milestone_create", s.workMilestoneCreate},
		{"PATCH " + w + "/milestones/{id}", signedIn, "work.milestone_update", s.workMilestoneUpdate},
		{"GET " + w + "/sprints", signedIn, "", s.workSprints},
		{"POST " + w + "/sprints", signedIn, "work.sprint_create", s.workSprintCreate},
		{"GET " + w + "/sprints/{id}", signedIn, "", s.workSprintGet},
		{"PATCH " + w + "/sprints/{id}", signedIn, "work.sprint_update", s.workSprintUpdate},
		{"POST " + w + "/sprints/{id}/start", signedIn, "work.sprint_start", s.workSprintStart},
		{"POST " + w + "/sprints/{id}/complete", signedIn, "work.sprint_complete", s.workSprintComplete},
		{"GET " + w + "/sprints/{id}/burndown", signedIn, "", s.workBurndown},
		{"GET " + w + "/charts/velocity", signedIn, "", s.workVelocity},
		{"GET " + w + "/charts/flow", signedIn, "", s.workFlow},
		{"GET " + w + "/charts/times", signedIn, "", s.workTimes},
		{"GET " + w + "/roadmap", signedIn, "", s.workRoadmap},
		{"GET " + w + "/release-notes", signedIn, "", s.workReleaseNotes},
		{"GET " + w + "/settings", signedIn, "", s.workGetSettings},
		{"PUT " + w + "/settings", signedIn, "work.settings", s.workPutSettings},
		{"GET " + w + "/inbox", signedIn, "", s.workInbox},
		{"POST " + w + "/inbox/read", signedIn, "work.inbox_read", s.workInboxRead},
		{"GET " + w + "/home", signedIn, "", s.workHome},
	}
}

func (s *Server) workBoard(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	b, err := s.work().Board(r.Context(), s.workCaller(r), work.BoardOptions{Scope: sc, Query: q.Get("q"), LaneBy: q.Get("lanes"), Sprint: q.Get("sprint")})
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) workMilestones(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	ms, err := s.work().Milestones(r.Context(), s.workCaller(r), sc)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"milestones": ms})
}

func (s *Server) workMilestoneCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope string `json:"scope"`
		work.MilestoneIn
	}
	if !decode(w, r, &in) {
		return
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := s.work().CreateMilestone(r.Context(), s.workCaller(r), sc, in.MilestoneIn)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), m.Title)
	writeJSON(w, http.StatusCreated, m)
}

func (s *Server) workMilestoneUpdate(w http.ResponseWriter, r *http.Request) {
	var in work.MilestoneIn
	if !decode(w, r, &in) {
		return
	}
	m, err := s.work().UpdateMilestone(r.Context(), s.workCaller(r), r.PathValue("id"), in)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, m.Title, "")
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) workSprints(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	sp, err := s.work().Sprints(r.Context(), s.workCaller(r), sc)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sprints": sp})
}

func (s *Server) workSprintCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope string `json:"scope"`
		work.SprintIn
	}
	if !decode(w, r, &in) {
		return
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sp, err := s.work().CreateSprint(r.Context(), s.workCaller(r), sc, in.SprintIn)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sc.String(), sp.Name)
	writeJSON(w, http.StatusCreated, sp)
}

func (s *Server) workSprintGet(w http.ResponseWriter, r *http.Request) {
	sp, err := s.work().GetSprint(r.Context(), s.workCaller(r), r.PathValue("id"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) workSprintUpdate(w http.ResponseWriter, r *http.Request) {
	var in work.SprintIn
	if !decode(w, r, &in) {
		return
	}
	sp, err := s.work().UpdateSprint(r.Context(), s.workCaller(r), r.PathValue("id"), in)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sp.Name, "")
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) workSprintStart(w http.ResponseWriter, r *http.Request) {
	sp, err := s.work().StartSprint(r.Context(), s.workCaller(r), r.PathValue("id"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sp.Name, "started")
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) workSprintComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CarryTo string `json:"carry_to"`
	}
	if !decode(w, r, &in) {
		return
	}
	sp, err := s.work().CompleteSprint(r.Context(), s.workCaller(r), r.PathValue("id"), in.CarryTo)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, sp.Name, "completed, carried to "+in.CarryTo)
	writeJSON(w, http.StatusOK, sp)
}

func (s *Server) workBurndown(w http.ResponseWriter, r *http.Request) {
	b, err := s.work().Burndown(r.Context(), s.workCaller(r), r.PathValue("id"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (s *Server) workVelocity(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("sprints"))
	v, err := s.work().Velocity(r.Context(), s.workCaller(r), sc, n)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workFlow(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("days"))
	v, err := s.work().CFD(r.Context(), s.workCaller(r), sc, n)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workTimes(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("days"))
	v, err := s.work().Times(r.Context(), s.workCaller(r), sc, n)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) workRoadmap(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	rows, err := s.work().Roadmap(r.Context(), s.workCaller(r), sc)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// workReleaseNotes returns the work finished between two moments, for a release.
func (s *Server) workReleaseNotes(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	var since, until time.Time
	for _, p := range []struct {
		key string
		dst *time.Time
	}{{"since", &since}, {"until", &until}} {
		if v := r.URL.Query().Get(p.key); v != "" {
			ms, err := work.ParseTime(v)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			*p.dst = time.UnixMilli(ms)
		}
	}
	rn, err := s.work().ClosedSince(r.Context(), s.workCaller(r), sc, since, until)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rn)
}

// workGetSettings returns the settings of a level, and what is in force.
func (s *Server) workGetSettings(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scopeParam(w, r)
	if !ok {
		return
	}
	s.writeSettings(w, r, sc)
}

func (s *Server) writeSettings(w http.ResponseWriter, r *http.Request, sc work.Scope) {
	c := s.workCaller(r)
	if !c.Read(sc) {
		writeErr(w, http.StatusNotFound, "no such project")
		return
	}
	own, err := s.work().GetSettings(r.Context(), sc)
	if err == nil {
		var eff *work.Effective
		if eff, err = s.work().Settings(r.Context(), sc); err == nil {
			var au *work.AutonomyStatus
			if au, err = s.work().Autonomy(r.Context(), sc); err == nil {
				writeJSON(w, http.StatusOK, map[string]any{"scope": sc, "own": own, "effective": eff, "autonomy": au, "can_change": c.Maintain(sc) && c.Human()})
				return
			}
		}
	}
	s.failWork(w, err)
}

// workPutSettings replaces the settings of a level; whoever maintains it may.
// Loosening what agents may do is recorded with its reason.
func (s *Server) workPutSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope    string        `json:"scope"`
		Settings work.Settings `json:"settings"`
	}
	if !decode(w, r, &in) {
		return
	}
	sc, err := parseWorkScope(in.Scope)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	c := s.workCaller(r)
	if c.Agent != nil || !c.Human() || !c.Read(sc) || !c.Maintain(sc) {
		writeErr(w, http.StatusForbidden, "only a person who maintains "+sc.String()+" changes how work is set up there")
		return
	}
	if err := s.work().PutSettings(r.Context(), sc, in.Settings, c.Actor); err != nil {
		s.failWork(w, err)
		return
	}
	detail := "settings"
	if a := in.Settings.Agents; a != nil {
		if a.Autonomy != nil {
			detail += ", agent autonomy level " + strconv.Itoa(a.Autonomy.Level) + ": " + a.Autonomy.Reason
		}
		if a.Review != nil {
			detail += ", agent review " + a.Review.Mode + ": " + a.Review.Reason
		}
		if a.Reason != "" {
			detail += ", " + a.Reason
		}
	}
	setAudit(r, sc.String(), detail)
	s.writeSettings(w, r, sc)
}

func (s *Server) workInbox(w http.ResponseWriter, r *http.Request) {
	c := s.workCaller(r)
	es, unread, err := s.work().Inbox(r.Context(), c.Name, r.URL.Query().Get("unread") == "true", 100)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": es, "unread": unread, "email": "not available: Launchway has no mail sender; entries appear here and on the home page, and notification channels carry them elsewhere"})
}

func (s *Server) workInboxRead(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	c := s.workCaller(r)
	if err := s.work().MarkRead(r.Context(), c.Name, in.IDs); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, c.Name, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// workHome is what the home page shows about work: the person's inbox count,
// what is assigned to them, what agents did and what waits for their review.
func (s *Server) workHome(w http.ResponseWriter, r *http.Request) {
	c := s.workCaller(r)
	st := s.work()
	mine, err := st.List(r.Context(), c, work.ListOptions{Query: "assignee:me is:open", Sort: "priority", Limit: 10})
	if err != nil {
		s.failWork(w, err)
		return
	}
	aw, err := st.AgentWorkFor(r.Context(), c)
	if err != nil {
		s.failWork(w, err)
		return
	}
	_, unread, _ := st.Inbox(r.Context(), c.Name, true, 1)
	samples, _ := st.Samples(r.Context(), c, true)
	writeJSON(w, http.StatusOK, map[string]any{"assigned": mine.Items, "assigned_total": mine.Total, "unread": unread, "agents": aw, "samples": samples})
}
