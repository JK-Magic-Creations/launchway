package api

import (
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/work"
)

// Time on work items: timers, manual entries, totals and a CSV export (internal/work/time.go has the rules).

func (s *Server) workTimeRoutes() []route {
	const b = "/api/v1/work/items"
	return []route{
		{"GET " + b + "/{ref}/time", signedIn, "", s.workTimeOf},
		{"POST " + b + "/{ref}/time", signedIn, "work.time_log", s.workTimeLog},
		{"POST " + b + "/{ref}/timer", signedIn, "work.timer_start", s.workTimerStart},
		{"GET /api/v1/work/timer", signedIn, "", s.workTimerGet},
		{"POST /api/v1/work/timer/stop", signedIn, "work.timer_stop", s.workTimerStop},
		{"PATCH /api/v1/work/time/{id}", signedIn, "work.time_edit", s.workTimeEdit},
		{"DELETE /api/v1/work/time/{id}", signedIn, "work.time_delete", s.workTimeDelete},
		{"GET /api/v1/work/time", signedIn, "", s.workTimeReport},
		{"GET /api/v1/work/time/export", signedIn, "", s.workTimeExport},
	}
}

func (s *Server) workTimeOf(w http.ResponseWriter, r *http.Request) {
	out, err := s.work().TimeOn(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) workTimeLog(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Minutes int    `json:"minutes"`
		Day     string `json:"day"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	e, err := s.work().LogTime(r.Context(), s.workCaller(r), r.PathValue("ref"), in.Minutes, in.Day, in.Note)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, e.Item, work.MinutesText(e.Minutes))
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) workTimerStart(w http.ResponseWriter, r *http.Request) {
	t, err := s.work().StartTimer(r.Context(), s.workCaller(r), r.PathValue("ref"))
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, t.Item, "")
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) workTimerGet(w http.ResponseWriter, r *http.Request) {
	t, err := s.work().RunningTimerOf(r.Context(), s.workCaller(r))
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"timer": t})
}

func (s *Server) workTimerStop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	e, err := s.work().StopTimer(r.Context(), s.workCaller(r), in.Note)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, e.Item, work.MinutesText(e.Minutes))
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) workTimeEdit(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Minutes *int    `json:"minutes"`
		Day     *string `json:"day"`
		Note    *string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Minutes == nil && in.Day == nil && in.Note == nil {
		writeErr(w, http.StatusBadRequest, "say what to change: minutes, day or note")
		return
	}
	e, err := s.work().EditTime(r.Context(), s.workCaller(r), r.PathValue("id"), in.Minutes, in.Day, in.Note)
	if err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, e.Item, work.MinutesText(e.Minutes))
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) workTimeDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.work().DeleteTime(r.Context(), s.workCaller(r), r.PathValue("id")); err != nil {
		s.failWork(w, err)
		return
	}
	setAudit(r, r.PathValue("id"), "")
	writeJSON(w, http.StatusOK, map[string]any{"deleted": r.PathValue("id")})
}

func (s *Server) timeQuery(w http.ResponseWriter, r *http.Request) (work.TimeQuery, bool) {
	q := r.URL.Query()
	tq := work.TimeQuery{Who: q.Get("person"), From: q.Get("from"), To: q.Get("to")}
	if tq.Who == "me" {
		tq.Who = s.workCaller(r).Name
	}
	if v := q.Get("scope"); v != "" {
		sc, err := parseWorkScope(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return tq, false
		}
		tq.Scope = &sc
	}
	return tq, true
}

func (s *Server) workTimeReport(w http.ResponseWriter, r *http.Request) {
	tq, ok := s.timeQuery(w, r)
	if !ok {
		return
	}
	rep, err := s.work().TimeReport(r.Context(), s.workCaller(r), tq)
	if err != nil {
		s.failWork(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) workTimeExport(w http.ResponseWriter, r *http.Request) {
	tq, ok := s.timeQuery(w, r)
	if !ok {
		return
	}
	rep, err := s.work().TimeReport(r.Context(), s.workCaller(r), tq)
	if err != nil {
		s.failWork(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="work-time.csv"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if rep.Truncated {
		w.Header().Set("X-Launchway-Truncated", "true")
	}
	_, _ = w.Write([]byte(work.TimeCSV(rep)))
}
