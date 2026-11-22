package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/release"
	"github.com/jk-magic-creations/launchway/internal/source"
)

// Release management across Airfields: the matrix, checklists, promotion,
// return to base, the calendar and freezes, formations and the report.

func (s *Server) releaseManageRoutes() []route {
	p := "/api/v1/projects/{name}"
	return []route{
		{"GET /api/v1/releases", signedIn, "", s.listAllReleases},
		{"GET /api/v1/release-matrix", signedIn, "", s.releaseMatrix},
		{"GET /api/v1/release-calendar", signedIn, "", s.releaseCalendar},
		{"GET /api/v1/release-report", signedIn, "", s.releaseReport},
		{"GET /api/v1/release-installation", signedIn, "", s.getReleaseInstallation},
		{"PUT /api/v1/release-installation", administer, "release.installation", s.putReleaseInstallation},
		{"GET /api/v1/release-freezes", signedIn, "", s.listFreezes},
		{"POST /api/v1/release-freezes", signedIn, "release.freeze", s.createFreeze},
		{"POST /api/v1/release-freezes/{id}/cancel", signedIn, "release.freeze.cancel", s.cancelFreeze},
		{"GET " + p + "/release-gates", projectRead, "", s.listGates},
		{"PUT " + p + "/release-gates/{airfield}", projectOperate, "release.gate", s.putGate},
		{"GET " + p + "/releases/{version}/checklist", projectRead, "", s.checklist},
		{"POST " + p + "/releases/{version}/clearances", projectOperate, "release.clearance", s.giveClearance},
		{"POST " + p + "/releases/{version}/promote", projectOperate, "release.promote", s.promote},
		{"POST " + p + "/releases/{version}/plan", projectOperate, "release.plan", s.planPromotion},
		{"POST " + p + "/release-returns", projectOperate, "release.return", s.returnToBase},
		{"GET /api/v1/release-formations", signedIn, "", s.listFormations},
		{"POST /api/v1/release-formations", signedIn, "release.formation.create", s.createFormation},
		{"GET /api/v1/release-formations/{id}", signedIn, "", s.getFormation},
		{"POST /api/v1/release-formations/{id}/promote", signedIn, "release.formation.promote", s.promoteFormation},
		{"POST /api/v1/release-formations/{id}/advance", signedIn, "release.formation.advance", s.advanceFormationRoute},
	}
}

func (s *Server) listAllReleases(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	q := r.URL.Query()
	list, err := s.Engine.ListReleases(engine.ReleaseFilter{State: q.Get("state"), Project: q.Get("project"), Visible: s.visibleFilter(r)})
	if err != nil {
		s.fail(w, err)
		return
	}
	out := list[:0]
	for _, x := range list {
		if x.State == engine.ReleaseDraft && !s.authorize(id, auth.Operate, x.Project) {
			continue
		}
		x.Notes = ""
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) releaseMatrix(w http.ResponseWriter, r *http.Request) {
	afs, rows, err := s.Engine.ReleaseMatrix(s.visibleFilter(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	// Which cells a person may promote into, and what to.
	id := identityFrom(r)
	type next struct {
		Project  string `json:"project"`
		Airfield string `json:"airfield"`
		Release  string `json:"release"`
		Version  string `json:"version"`
	}
	var suggestions []next
	for _, row := range rows {
		if !s.authorize(id, auth.Operate, row.Project) || row.Latest == "" {
			continue
		}
		for _, c := range row.Cells {
			if c.Version != row.Latest && c.Launching == nil && s.envRefusal(id, row.Project, c.Airfield) == "" {
				if rel, err := s.Engine.FindRelease(row.Project, row.Latest); err == nil {
					suggestions = append(suggestions, next{row.Project, c.Airfield, rel.ID, rel.Version})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"airfields": afs, "rows": rows, "can_promote": suggestions})
}

func (s *Server) getReleaseInstallation(w http.ResponseWriter, r *http.Request) {
	ok, err := s.Engine.PublicReleasesAllowed()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_downloads": ok, "may_change": s.authorize(identityFrom(r), auth.Administer, "")})
}

func (s *Server) putReleaseInstallation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublicDownloads bool `json:"public_downloads"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, "installation", fmt.Sprintf("public downloads %v", in.PublicDownloads))
	if err := s.Engine.SetPublicReleasesAllowed(in.PublicDownloads, identityFrom(r).User); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_downloads": in.PublicDownloads})
}

// ---- gates ----

func (s *Server) listGates(w http.ResponseWriter, r *http.Request) {
	g, err := s.Engine.ReleaseGates(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) putGate(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	if !s.isProjectAdmin(identityFrom(r), project) {
		writeErr(w, http.StatusForbidden, "you need the admin role on "+project+" to change what a release must pass to reach an Airfield")
		return
	}
	var g release.Gate
	if !decode(w, r, &g) {
		return
	}
	setAudit(r, project+" → "+r.PathValue("airfield"), fmt.Sprintf("checks %v notes %v findings %v soak %dm clearances %d previous %q window %v",
		g.RequireChecks, g.RequireNotes, g.RequireNoBlock, g.SoakMinutes, g.Clearances, g.PreviousAirfield, g.Window != nil))
	got, err := s.Engine.SetReleaseGate(project, r.PathValue("airfield"), g, identityFrom(r).User)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) checklist(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	af := r.URL.Query().Get("airfield")
	if af == "" {
		writeErr(w, http.StatusBadRequest, "say which Airfield: ?airfield=prod")
		return
	}
	ck, err := s.Engine.ReleaseChecklist(rel.ID, af)
	if err != nil {
		s.fail(w, err)
		return
	}
	id := identityFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{"checklist": ck, "may_promote": s.authorize(id, auth.Operate, rel.Project) && s.envRefusal(id, rel.Project, af) == "",
		"may_override": s.isProjectAdmin(id, rel.Project) && ck.CanOverride, "environment_refusal": s.envRefusal(id, rel.Project, af)})
}

func (s *Server) giveClearance(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Airfield string `json:"airfield"`
		Comment  string `json:"comment"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	setAudit(r, releaseTarget(rel)+" → "+in.Airfield, clip(in.Comment, 300))
	if msg := s.envRefusal(id, rel.Project, in.Airfield); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	c, err := s.Engine.GiveReleaseClearance(rel.ID, in.Airfield, id.User, s.roleName(id, rel.Project), in.Comment)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// ---- promotion ----

// promotion is what the person asked for.
type promotion struct {
	Release   *engine.Release
	Airfield  string
	Override  string
	Actor     string
	IsAdmin   bool
	Formation string
	Planned   int64
}

// promoteOne checks the checklist, the tag and the pipeline, starts the
// Sortie and records the promotion. It returns the promotion and an HTTP
// status that says what went wrong, with words.
func (s *Server) promoteOne(ctx context.Context, p promotion) (*engine.ReleasePromotion, *engine.RunSummary, int, error) {
	rel := p.Release
	if s.Projects == nil && s.Releases.Start == nil {
		return nil, nil, http.StatusNotFound, errors.New("no Flights are configured")
	}
	ck, err := s.Engine.ReleaseChecklist(rel.ID, p.Airfield)
	if err != nil {
		return nil, nil, 0, err
	}
	var overridden []string
	if !ck.Ready {
		list, over := release.Blockers(ck.Items)
		for _, b := range list {
			overridden = append(overridden, b.ID)
		}
		switch {
		case p.Override == "":
			return nil, nil, http.StatusConflict, fmt.Errorf("the checklist of %s is not met: %s", p.Airfield, blockerText(list))
		case !over:
			return nil, nil, http.StatusConflict, fmt.Errorf("the checklist of %s has lines that cannot be overridden: %s", p.Airfield, blockerText(list))
		case !p.IsAdmin:
			return nil, nil, http.StatusForbidden, fmt.Errorf("only an administrator of %s may override the checklist (%s); ask for the admin role, or wait until it is met", rel.Project, blockerText(list))
		}
	} else if p.Override != "" {
		overridden = nil
	}
	if ck.AlreadyThere {
		return nil, nil, http.StatusConflict, fmt.Errorf("%s %s already runs in %s and is healthy", rel.Project, rel.Version, p.Airfield)
	}
	// The tag must still point to the commit that was released.
	if rel.Tag == "" {
		return nil, nil, http.StatusConflict, errors.New("the release has no tag yet: publish it first")
	}
	if s.Projects != nil {
		ti, err := s.Projects.ReleaseTag(ctx, rel.Project, rel.Tag)
		if err != nil {
			return nil, nil, 0, err
		}
		if !ti.Exists || ti.Commit != rel.SHA {
			return nil, nil, http.StatusConflict, fmt.Errorf("the tag %s no longer points to the released commit %s; nothing is promoted until it is back (a tag is never moved: if it was, release a new version)", rel.Tag, rel.SHA[:8])
		}
	}
	inputs, code, err := s.promotionInputs(ctx, rel, p.Airfield)
	if err != nil {
		return nil, nil, code, err
	}
	rq := source.StartRequest{Project: rel.Project, Ref: rel.Tag, Actor: p.Actor, Inputs: inputs}
	start := s.Releases.Start
	if start == nil {
		start = s.Projects.StartRunWith
	}
	run, err := start(ctx, rq)
	if err != nil {
		return nil, nil, 0, err
	}
	if run.SHA != "" && run.SHA != rel.SHA {
		// Another commit than the release's must never be deployed under its name: the Sortie is stopped.
		_ = s.Engine.Cancel(run.ID, p.Actor)
		return nil, nil, http.StatusConflict, fmt.Errorf("the Sortie ran at %s, not the release's commit %s; it was canceled", run.SHA[:8], rel.SHA[:8])
	}
	rec, err := s.Engine.RecordPromotion(engine.ReleasePromotion{ReleaseID: rel.ID, Project: rel.Project, Version: rel.Version, Airfield: p.Airfield, SHA: rel.SHA, //nolint:contextcheck // a notification outlives the request
		Digests: rel.Digests, RunID: run.ID, RunNumber: run.Number, FormationID: p.Formation, RequestedBy: p.Actor, Override: p.Override, Overridden: strings.Join(overridden, ","), State: "started"})
	return rec, run, 0, err
}

// promotionInputs gives the pipeline what the release is: the same commit, the same digest, as the inputs it declares.
func (s *Server) promotionInputs(ctx context.Context, rel *engine.Release, airfield string) (map[string]string, int, error) {
	// Give the pipeline what the release is: the same commit, the same digest.
	inputs := map[string]string{}
	declared := map[string]bool{}
	if s.Projects != nil {
		info, err := s.Projects.InputsAt(ctx, rel.Project, rel.Tag)
		if err != nil {
			return nil, 0, err
		}
		if len(info.Problems) > 0 {
			return nil, http.StatusUnprocessableEntity, fmt.Errorf("the pipeline at %s has problems: %s", rel.Tag, info.Problems[0].Message)
		}
		for _, in := range info.Inputs {
			declared[in.Name] = true
		}
	}
	digests := make([]string, 0, len(rel.Digests))
	for _, d := range rel.Digests {
		digests = append(digests, d.Digest)
	}
	want := map[string]string{"release": rel.Version, "version": rel.Version, "airfield": airfield, "sha": rel.SHA}
	if len(digests) > 0 {
		want["digest"], want["digests"] = digests[0], strings.Join(digests, ",")
	}
	for k, v := range want {
		if declared[k] {
			inputs[k] = v
		}
	}
	if s.Projects != nil && !declared["airfield"] && !declared["release"] && !declared["version"] {
		return nil, http.StatusConflict, fmt.Errorf("the pipeline of %s takes no input named airfield, release or version, so Launchway cannot tell it what to promote or where: add inputs airfield and release (and digest for the image) to .launchway/pipeline.yaml, as the guide \"Promoting a release\" shows", rel.Project)
	}
	return inputs, 0, nil
}

func blockerText(items []release.Item) string {
	var parts []string
	for _, i := range items {
		parts = append(parts, i.Label+" ("+i.Detail+")")
	}
	return strings.Join(parts, "; ")
}

func (s *Server) promote(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Airfield string `json:"airfield"`
		Override string `json:"override"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	setAudit(r, releaseTarget(rel)+" → "+in.Airfield, clip("promote; override: "+in.Override, 300))
	if in.Airfield == "" {
		writeErr(w, http.StatusBadRequest, "say which Airfield: {\"airfield\": \"prod\"}")
		return
	}
	if msg := s.envRefusal(id, rel.Project, in.Airfield); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	pr, run, code, err := s.promoteOne(r.Context(), promotion{Release: rel, Airfield: in.Airfield, Override: strings.TrimSpace(in.Override), Actor: id.User, IsAdmin: s.isProjectAdmin(id, rel.Project)})
	if code != 0 {
		var ck *engine.Checklist
		if code == http.StatusConflict || code == http.StatusForbidden {
			ck, _ = s.Engine.ReleaseChecklist(rel.ID, in.Airfield)
		}
		writeJSON(w, code, map[string]any{"error": err.Error(), "checklist": ck})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, releaseTarget(rel)+" → "+in.Airfield, clip(fmt.Sprintf("run #%d %s at %s, digests %d; override: %s", run.Number, run.ID, rel.SHA[:8], len(rel.Digests), in.Override), 400))
	writeJSON(w, http.StatusCreated, map[string]any{"promotion": pr, "run": run})
}

// planPromotion writes a promotion on the calendar as a reminder; promoting stays a person's action.
func (s *Server) planPromotion(w http.ResponseWriter, r *http.Request) {
	rel, ok := s.releaseOf(w, r)
	if !ok {
		return
	}
	var in struct {
		Airfield string `json:"airfield"`
		At       string `json:"at"`
		Note     string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	at, err := parseTime(in.At, time.Time{})
	if err != nil || at.IsZero() {
		writeErr(w, http.StatusBadRequest, "say when: {\"airfield\": \"prod\", \"at\": \"2026-11-24T14:00:00Z\"}")
		return
	}
	id := identityFrom(r)
	if msg := s.envRefusal(id, rel.Project, in.Airfield); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	if s.Engine.AirfieldKnown(in.Airfield) != nil {
		writeErr(w, http.StatusNotFound, "there is no Airfield "+in.Airfield)
		return
	}
	setAudit(r, releaseTarget(rel)+" → "+in.Airfield, "planned for "+at.UTC().Format(time.RFC3339))
	pr, err := s.Engine.RecordPromotion(engine.ReleasePromotion{ReleaseID: rel.ID, Project: rel.Project, Version: rel.Version, Airfield: in.Airfield, SHA: rel.SHA, //nolint:contextcheck // a notification outlives the request
		PlannedFor: at.UnixMilli(), RequestedBy: id.User, State: "planned", Detail: clip(in.Note, 300)})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pr)
}

// returnToBase puts an Airfield back on the release before the one it runs, through the existing rollback.
func (s *Server) returnToBase(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("name")
	var in struct {
		Airfield string `json:"airfield"`
		To       string `json:"to"`
		Reason   string `json:"reason"`
		Queue    bool   `json:"queue"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	setAudit(r, project+" in "+in.Airfield, clip("return to base: "+in.Reason, 300))
	if in.Airfield == "" {
		writeErr(w, http.StatusBadRequest, "say which Airfield: {\"airfield\": \"prod\", \"reason\": \"...\"}")
		return
	}
	if msg := s.envRefusal(id, project, in.Airfield); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	to := in.To
	if to == "" {
		plan, err := s.Engine.RollbackPlan(project, in.Airfield, "")
		if err != nil {
			s.fail(w, err)
			return
		}
		if len(plan.Action.Targets) == 0 {
			writeErr(w, http.StatusConflict, nonEmpty(plan.Action.Reason, "there is no earlier release to return to in "+in.Airfield))
			return
		}
		to = plan.Action.Targets[0].Deployment
	}
	sum, err := s.Engine.RequestRollback(engine.RollbackRequest{Project: project, Environment: in.Airfield, To: to, Reason: in.Reason, Actor: id.User, Queue: in.Queue})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Engine.NoticeReleaseReturned(project, in.Airfield, "", to, id.User, in.Reason, sum.ID) //nolint:contextcheck // a notification outlives the request
	writeJSON(w, http.StatusCreated, sum)
}

// ---- freezes and the calendar ----

func (s *Server) listFreezes(w http.ResponseWriter, r *http.Request) {
	since := s.Now().Add(-30 * 24 * time.Hour).UnixMilli()
	list, err := s.Engine.ReleaseFreezes(since)
	if err != nil {
		s.fail(w, err)
		return
	}
	vis := s.visibleFilter(r)
	out := []release.Freeze{}
	for _, f := range list {
		if f.Project == "" || vis(f.Project) {
			out = append(out, f)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createFreeze(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project  string `json:"project"`
		Airfield string `json:"airfield"`
		From     string `json:"from"`
		To       string `json:"to"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	setAudit(r, nonEmpty(in.Project, "every Flight")+" / "+nonEmpty(in.Airfield, "every Airfield"), clip(in.Reason, 300))
	if in.Project == "" && !s.authorize(id, auth.Administer, "") {
		writeErr(w, http.StatusForbidden, "only an administrator of every {project} may freeze every {project}; freeze one {project} you administer instead")
		return
	}
	if in.Project != "" && !s.isProjectAdmin(id, in.Project) {
		writeErr(w, http.StatusForbidden, "you need the admin role on "+in.Project+" to freeze it")
		return
	}
	now := s.Now()
	from, err := parseTime(in.From, now)
	if err == nil {
		var to time.Time
		if to, err = parseTime(in.To, time.Time{}); err == nil {
			var f *release.Freeze
			if f, err = s.Engine.CreateReleaseFreeze(in.Project, in.Airfield, from, to, in.Reason, id.User); err == nil {
				writeJSON(w, http.StatusCreated, f)
				return
			}
		}
	}
	s.fail(w, err)
}

func (s *Server) cancelFreeze(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	list, err := s.Engine.ReleaseFreezes(0)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, f := range list {
		if f.ID != r.PathValue("id") {
			continue
		}
		okay := (f.Project == "" && s.authorize(id, auth.Administer, "")) || (f.Project != "" && s.isProjectAdmin(id, f.Project))
		if !okay {
			writeErr(w, http.StatusForbidden, "you may not cancel this freeze: it takes the admin role on its {project} (or on every {project} for a freeze of all)")
			return
		}
		setAudit(r, f.ID, "canceled")
		if err := s.Engine.CancelReleaseFreeze(f.ID, id.User); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeErr(w, http.StatusNotFound, "no such freeze")
}

// CalendarEntry is a line of the release calendar.
type CalendarEntry struct {
	At       int64  `json:"at"`
	End      int64  `json:"end,omitempty"`
	Kind     string `json:"kind"` // planned, in_progress, done, freeze
	Project  string `json:"project,omitempty"`
	Airfield string `json:"airfield,omitempty"`
	Version  string `json:"version,omitempty"`
	Release  string `json:"release,omitempty"`
	Title    string `json:"title"`
	By       string `json:"by,omitempty"`
	State    string `json:"state,omitempty"`
}

func (s *Server) releaseCalendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := s.Now()
	from, err := parseTime(q.Get("from"), now.Add(-14*24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	to, err := parseTime(q.Get("to"), now.Add(30*24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	vis := s.visibleFilter(r)
	out := []CalendarEntry{}
	proms, err := s.Engine.ReleasePromotions("", q.Get("project"), 500)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, p := range proms {
		if !vis(p.Project) {
			continue
		}
		at := p.RequestedAt
		kind := "done"
		switch p.State {
		case "planned":
			at, kind = p.PlannedFor, "planned"
		case "started":
			kind = "in_progress"
		}
		if at < from.UnixMilli() || at > to.UnixMilli() {
			continue
		}
		title := fmt.Sprintf("%s %s → %s", p.Project, p.Version, p.Airfield)
		out = append(out, CalendarEntry{At: at, Kind: kind, Project: p.Project, Airfield: p.Airfield, Version: p.Version, Release: p.ReleaseID, Title: title, By: p.RequestedBy, State: p.State})
	}
	frz, err := s.Engine.ReleaseFreezes(from.UnixMilli())
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, f := range frz {
		if f.From > to.UnixMilli() || (f.Project != "" && !vis(f.Project)) {
			continue
		}
		where := nonEmpty(f.Airfield, "every Airfield")
		st := "in force"
		switch {
		case f.Canceled:
			st = "canceled"
		case f.To < now.UnixMilli():
			st = "over"
		case f.From > now.UnixMilli():
			st = "coming"
		}
		out = append(out, CalendarEntry{At: f.From, End: f.To, Kind: "freeze", Project: f.Project, Airfield: f.Airfield, Title: "Freeze of " + where + ": " + f.Reason, By: f.By, State: st})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	writeJSON(w, http.StatusOK, map[string]any{"from": from.UnixMilli(), "to": to.UnixMilli(), "now": now.UnixMilli(), "entries": out})
}

func (s *Server) releaseReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := s.Now()
	to, err := parseTime(q.Get("to"), now)
	if err != nil {
		s.fail(w, err)
		return
	}
	from, err := parseTime(q.Get("from"), to.Add(-90*24*time.Hour))
	if err != nil {
		s.fail(w, err)
		return
	}
	project := q.Get("project")
	if project != "" && !s.authorize(identityFrom(r), auth.Read, project) {
		writeErr(w, http.StatusNotFound, "project "+project+" not found")
		return
	}
	rep, err := s.Engine.BuildReleaseReport(project, from, to, s.visibleFilter(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	if q.Get("format") == "html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:")
		w.Header().Set("Content-Disposition", `attachment; filename="release-report.html"`)
		_, _ = w.Write([]byte(renderReportHTML(rep)))
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- formations ----

func (s *Server) listFormations(w http.ResponseWriter, r *http.Request) {
	list, err := s.Engine.ListFormations(100)
	if err != nil {
		s.fail(w, err)
		return
	}
	vis := s.visibleFilter(r)
	out := []engine.Formation{}
	for _, f := range list {
		okay := true
		for _, p := range f.FormationProjects() {
			okay = okay && vis(p)
		}
		if okay {
			out = append(out, f)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) formationAccess(w http.ResponseWriter, r *http.Request, f *engine.Formation, a auth.Action) bool {
	id := identityFrom(r)
	for _, p := range f.FormationProjects() {
		if !s.authorize(id, auth.Read, p) {
			writeErr(w, http.StatusNotFound, "formation "+f.Name+" not found")
			return false
		}
		if a == auth.Operate && !s.authorize(id, auth.Operate, p) {
			writeErr(w, http.StatusForbidden, "you need the operator role on "+p+" to promote this formation, which includes a release of it")
			return false
		}
	}
	return true
}

func (s *Server) createFormation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string   `json:"name"`
		Note     string   `json:"note"`
		Releases []string `json:"releases"` // "project@version", in the order they are promoted
		// Order is "explicit" (default: the order of releases) or "dependencies": the releases are put in the software
		// graph's dependency order, providers first, whatever order they are written in.
		Order string `json:"order"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	setAudit(r, in.Name, strings.Join(in.Releases, ", "))
	var ids []string
	for _, ref := range in.Releases {
		project, version, ok := strings.Cut(ref, "@")
		if !ok || project == "" || version == "" {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("write each release as {project}@version, such as api@2.0.0; %q is not", ref))
			return
		}
		if !s.authorize(id, auth.Operate, project) {
			writeErr(w, http.StatusForbidden, "you need the operator role on "+project+" to put its release in a formation")
			return
		}
		rel, err := s.Engine.FindRelease(project, version)
		if err != nil {
			s.fail(w, err)
			return
		}
		ids = append(ids, rel.ID)
	}
	source := engine.OrderExplicit
	switch in.Order {
	case "", engine.OrderExplicit:
	case engine.OrderDependencies:
		ordered, err := s.formationByDependencies(r.Context(), id, ids)
		if err != nil {
			s.fail(w, err)
			return
		}
		ids, source = ordered, engine.OrderDependencies
	default:
		writeErr(w, http.StatusBadRequest, `order is "explicit" or "dependencies"`)
		return
	}
	f, err := s.Engine.CreateFormationOrdered(in.Name, in.Note, ids, id.User, source)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (s *Server) getFormation(w http.ResponseWriter, r *http.Request) {
	f, err := s.Engine.GetFormation(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.formationAccess(w, r, f, auth.Read) {
		return
	}
	// Looking at a formation moves it on when the member before has launched.
	if f.State == engine.FormationPromoting {
		s.advanceFormation(r.Context(), f)
		f, _ = s.Engine.GetFormation(f.ID)
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) promoteFormation(w http.ResponseWriter, r *http.Request) {
	f, err := s.Engine.GetFormation(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.formationAccess(w, r, f, auth.Operate) {
		return
	}
	var in struct {
		Airfield string `json:"airfield"`
		Override string `json:"override"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := identityFrom(r)
	setAudit(r, f.Name+" → "+in.Airfield, "promote the formation; override: "+clip(in.Override, 200))
	if f.State != engine.FormationPlanned {
		writeErr(w, http.StatusConflict, fmt.Sprintf("the formation is %s; a formation is promoted once (make another to promote again)", f.State))
		return
	}
	// All or nothing to begin with: every member's checklist must pass before the first is started.
	type blocked struct {
		Project string `json:"project"`
		Version string `json:"version"`
		Why     string `json:"why"`
	}
	var bl []blocked
	for _, m := range f.Members {
		if msg := s.envRefusal(id, m.Project, in.Airfield); msg != "" {
			writeErr(w, http.StatusForbidden, msg)
			return
		}
		ck, err := s.Engine.ReleaseChecklist(m.ReleaseID, in.Airfield)
		if err != nil {
			s.fail(w, err)
			return
		}
		if ck.Ready {
			continue
		}
		list, over := release.Blockers(ck.Items)
		if in.Override != "" && over && s.isProjectAdmin(id, m.Project) {
			continue
		}
		bl = append(bl, blocked{m.Project, m.Version, blockerText(list)})
	}
	if len(bl) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "nothing was started: at least one release of the formation does not pass the checklist of " + in.Airfield, "blocked": bl})
		return
	}
	if err := s.Engine.SetFormation(f.ID, engine.FormationPromoting, in.Airfield); err != nil {
		s.fail(w, err)
		return
	}
	f, _ = s.Engine.GetFormation(f.ID)
	s.advanceWith(r.Context(), f, id.User, strings.TrimSpace(in.Override), true)
	f, _ = s.Engine.GetFormation(f.ID)
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) advanceFormationRoute(w http.ResponseWriter, r *http.Request) {
	f, err := s.Engine.GetFormation(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if !s.formationAccess(w, r, f, auth.Operate) {
		return
	}
	s.advanceFormation(r.Context(), f)
	f, _ = s.Engine.GetFormation(f.ID)
	writeJSON(w, http.StatusOK, f)
}

// advanceFormation moves a promoting formation on, as the person who started it.
func (s *Server) advanceFormation(ctx context.Context, f *engine.Formation) {
	actor, override := "launchway", ""
	for _, m := range f.Members {
		if m.PromotionID != "" {
			if p, err := s.Engine.Promotion(m.PromotionID); err == nil {
				actor, override = p.RequestedBy, p.Override
			}
			break
		}
	}
	s.advanceWith(ctx, f, actor, override, false)
}

// advanceWith reads where each member stands and starts the next that is due. One failed
// member ends the formation: the members after it are not started and the report says so.
func (s *Server) advanceWith(ctx context.Context, f *engine.Formation, actor, override string, first bool) {
	if f.State != engine.FormationPromoting {
		return
	}
	launched, failed := 0, false
	for i := range f.Members {
		m := &f.Members[i]
		switch m.State {
		case engine.MemberStarted:
			p, err := s.Engine.Promotion(m.PromotionID)
			if err != nil {
				continue
			}
			switch p.State {
			case "launched":
				m.State = engine.MemberLaunched
				_ = s.Engine.SetFormationMember(f.ID, m.ReleaseID, engine.MemberLaunched, "", "")
			case "failed", "canceled", "returned":
				m.State = engine.MemberFailed
				_ = s.Engine.SetFormationMember(f.ID, m.ReleaseID, engine.MemberFailed, "", "the Sortie "+p.State)
			}
		}
		switch m.State {
		case engine.MemberLaunched:
			launched++
			continue
		case engine.MemberFailed:
			failed = true
		case engine.MemberStarted:
			return // wait for it
		case engine.MemberWaiting:
			if failed {
				_ = s.Engine.SetFormationMember(f.ID, m.ReleaseID, engine.MemberNotStarted, "", "an earlier release of the formation failed")
				continue
			}
			rel, err := s.Engine.GetRelease(m.ReleaseID)
			if err == nil {
				admin := override != "" // the override was checked when the formation was promoted
				var pr *engine.ReleasePromotion
				var code int
				pr, _, code, err = s.promoteOne(ctx, promotion{Release: rel, Airfield: f.Airfield, Override: override, Actor: actor, IsAdmin: admin, Formation: f.ID})
				if err == nil && code == 0 && pr != nil {
					m.State = engine.MemberStarted
					_ = s.Engine.SetFormationMember(f.ID, m.ReleaseID, engine.MemberStarted, pr.ID, "")
					return // one at a time, in order
				}
			}
			m.State = engine.MemberFailed
			failed = true
			_ = s.Engine.SetFormationMember(f.ID, m.ReleaseID, engine.MemberFailed, "", errText(err))
		case engine.MemberNotStarted:
		}
	}
	switch {
	case failed && launched > 0:
		_ = s.Engine.SetFormation(f.ID, engine.FormationPartial, "")
	case failed:
		_ = s.Engine.SetFormation(f.ID, engine.FormationFailed, "")
	case launched == len(f.Members):
		_ = s.Engine.SetFormation(f.ID, engine.FormationDone, "")
	}
	_ = first
}

func errText(err error) string {
	if err == nil {
		return "could not start"
	}
	return clip(err.Error(), 300)
}

// RunReleaseWork moves promoting formations on in the background until ctx ends.
func (s *Server) RunReleaseWork(ctx context.Context) {
	go s.runOutsideWork(ctx) // publishing on an outside Git host can take long: it has its own loop
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			list, err := s.Engine.ListFormations(100)
			if err != nil {
				continue
			}
			for i := range list {
				if list[i].State == engine.FormationPromoting {
					s.advanceFormation(ctx, &list[i])
				}
			}
		}
	}
}
