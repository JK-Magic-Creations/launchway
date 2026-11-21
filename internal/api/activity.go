package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// The trail: what a person opened and did, so they can pick up where they left off. It is the caller's own and nobody
// else's: every route works on the signed-in person and none takes a user name, so an administrator cannot read it either.
// Entries for projects the person can no longer see are left out, retention defaults to 90 days (the shared layout page
// "activity-settings" at the Base may set retention_days), and the person can pause or clear it.

func (s *Server) activityRoutes() []route {
	return []route{
		{"GET /api/v1/activity", signedIn, "", s.activityList},
		{"PUT /api/v1/activity", signedIn, "", s.activityRecord},
		{"DELETE /api/v1/activity", signedIn, "prefs.activity.clear", s.activityClear},
		{"PUT /api/v1/activity/pause", signedIn, "prefs.activity.pause", s.activityPause},
	}
}

const defaultRetentionDays = 90

func (s *Server) retentionDays() int {
	ls, _ := s.Engine.SharedLayouts("activity-settings", []string{"base"})
	for _, l := range ls {
		var d struct {
			Days int `json:"retention_days"`
		}
		if json.Unmarshal([]byte(l.Data), &d) == nil && d.Days > 0 && d.Days <= 3650 {
			return d.Days
		}
	}
	return defaultRetentionDays
}

type activityView struct {
	engine.Activity
	NowState string `json:"now_state,omitempty"`
	Changed  string `json:"changed,omitempty"` // what happened since the person was there, in words
}

// visibleActivity is the person's own trail with what they can no longer see left out, and what changed since they were there.
func (s *Server) visibleActivity(id *identity, limit, days int) ([]activityView, error) {
	entries, err := s.Engine.ActivityOf(id.User, days, 400)
	if err != nil {
		return nil, err
	}
	only, err := s.visibleProjects(id)
	if err != nil {
		return nil, err
	}
	admin := s.authorize(id, auth.Administer, "")
	out := make([]activityView, 0, len(entries))
	for _, a := range entries {
		if a.Project != "" && only != nil && !contains(only, a.Project) {
			continue // the person can no longer see it
		}
		if a.Kind == "settings" && !admin {
			continue
		}
		v := activityView{Activity: a}
		if a.Kind == "run" {
			if sum, err := s.Engine.Summary(a.Obj); err == nil {
				v.NowState = string(sum.Status)
				v.Changed = changedSince(a.State, string(sum.Status), sum.Number)
			}
		}
		out = append(out, v)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *Server) activityList(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	days := s.retentionDays()
	out, err := s.visibleActivity(id, limit, days)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Continue: what they started or changed lately, then the last things they looked at.
	cont := []activityView{}
	for _, v := range out {
		if v.Action != "viewed" && len(cont) < 4 {
			cont = append(cont, v)
		}
	}
	for _, v := range out {
		if len(cont) >= 4 {
			break
		}
		if v.Action == "viewed" && v.Kind != "page" && !hasEntry(cont, v) {
			cont = append(cont, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"continue": cont, "recent": out, "paused": s.prefsOf(id.User).TrailPaused, "retention_days": days})
}

func hasEntry(l []activityView, v activityView) bool {
	for _, x := range l {
		if x.Kind == v.Kind && x.Obj == v.Obj {
			return true
		}
	}
	return false
}

// changedSince says what happened to a Sortie since the person last saw it, or "" when nothing did.
func changedSince(before, now string, number int) string {
	if before == "" || before == now {
		return ""
	}
	switch now {
	case "succeeded":
		return "Sortie #" + strconv.Itoa(number) + " has finished: it passed."
	case "failed":
		return "Sortie #" + strconv.Itoa(number) + " has finished: it failed."
	case "canceled":
		return "Sortie #" + strconv.Itoa(number) + " was canceled."
	}
	return "Sortie #" + strconv.Itoa(number) + " is now " + now + "."
}

func (s *Server) activityRecord(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	var in engine.Activity
	if !decode(w, r, &in) {
		return
	}
	if id.User == "" || s.prefsOf(id.User).TrailPaused {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A Sortie belongs to the project it ran in, which decides who may see the entry.
	if in.Kind == "run" && in.Project == "" {
		sum, err := s.Engine.Summary(in.Obj)
		if err != nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		in.Project = sum.Project
		in.State = string(sum.Status)
	}
	// A project the person cannot see is not recorded, so a trail never names what they could not open.
	if in.Project != "" && !s.authorize(id, auth.Read, in.Project) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	if err := s.Engine.RecordActivity(id.User, in, s.retentionDays()); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) activityClear(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.ClearActivity(identityFrom(r).User); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

func (s *Server) activityPause(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	var in struct {
		Paused bool `json:"paused"`
	}
	if !decode(w, r, &in) || id.User == "" {
		return
	}
	p := s.prefsOf(id.User)
	p.TrailPaused = in.Paused
	if err := s.savePrefs(id.User, p); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": in.Paused})
}

// captured keeps the start of a success response so a wrapper can read what the handler made.
type captured struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *captured) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captured) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.body.Len() < 8192 {
		c.body.Write(b[:min(len(b), 8192-c.body.Len())])
	}
	return c.ResponseWriter.Write(b)
}

// startRunTrail is POST /api/v1/projects/{name}/runs: it starts the run and notes in the person's trail that they did.
func (s *Server) startRunTrail(w http.ResponseWriter, r *http.Request) {
	c := &captured{ResponseWriter: w}
	s.startRun(c, r)
	id := identityFrom(r)
	if c.status != http.StatusCreated && c.status != http.StatusOK || id.User == "" || s.prefsOf(id.User).TrailPaused {
		return
	}
	var run struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Status string `json:"status"`
	}
	if json.Unmarshal(c.body.Bytes(), &run) != nil || run.ID == "" {
		return
	}
	project := r.PathValue("name")
	_ = s.Engine.RecordActivity(id.User, engine.Activity{Kind: "run", Obj: run.ID, Project: project, Title: project + " #" + strconv.Itoa(run.Number), Href: "/runs/" + run.ID, Action: "started", State: "running"}, s.retentionDays())
}
