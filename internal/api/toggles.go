package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
	"github.com/jk-magic-creations/launchway/internal/toggles"
	"github.com/jk-magic-creations/launchway/internal/togglestore"
)

// Feature toggles (schema 47; internal/toggles, internal/togglestore).
//
// Toggles belong to a project, or to a unit, the organization or the
// installation (the levels variables have), and the routes below name the
// project whose view of them is wanted. Who may do what:
//
//	read            a viewer of the project
//	define, edit    an operator of the project for a toggle of the project; an
//	                administrator of the level for a shared one
//	change a state  an operator in the environment (the role on the
//	                environment, not only on the project), under the
//	                safeguards of the level
//	approve         an operator in the environment, who is not the person who
//	                asked (and a reviewer of the environment, if it names some)
//	keys            an administrator of the project; operators list them
//	safeguards      an administrator of the project, or of the level
//
// The routes for applications (SDK keys) are in toggles_sdk.go.

func (s *Server) toggleRoutes() []route {
	return []route{
		{"GET /api/v1/projects/{name}/toggles", projectRead, "", s.listToggles},
		{"POST /api/v1/projects/{name}/toggles", projectOperate, "toggle.create", s.createToggle},
		{"GET /api/v1/projects/{name}/toggle-changes", projectRead, "", s.toggleChanges},
		{"POST /api/v1/projects/{name}/toggle-changes/{id}/revert", projectOperate, "toggle.revert", s.revertToggleChange},
		{"POST /api/v1/projects/{name}/toggle-changes/{id}/approve", projectOperate, "toggle.approve", s.decideToggleChange("approve")},
		{"POST /api/v1/projects/{name}/toggle-changes/{id}/reject", projectOperate, "toggle.reject", s.decideToggleChange("reject")},
		{"POST /api/v1/projects/{name}/toggle-changes/{id}/withdraw", projectOperate, "toggle.withdraw", s.decideToggleChange("withdraw")},
		{"GET /api/v1/projects/{name}/toggle-timeline", projectRead, "", s.toggleTimeline},
		{"POST /api/v1/projects/{name}/toggle-revert-since", projectOperate, "toggle.revert_since", s.revertTogglesSince},
		{"GET /api/v1/projects/{name}/toggle-schedules", projectRead, "", s.toggleSchedules},
		{"DELETE /api/v1/projects/{name}/toggle-schedules/{id}", projectOperate, "toggle.schedule_cancel", s.cancelToggleSchedule},
		{"GET /api/v1/projects/{name}/toggle-file", projectRead, "", s.exportToggles},
		{"POST /api/v1/projects/{name}/toggle-plan", projectRead, "toggle.plan", s.planToggles},
		{"POST /api/v1/projects/{name}/toggle-apply", projectOperate, "toggle.apply", s.applyToggles},
		{"GET /api/v1/projects/{name}/toggles/{key}", projectRead, "", s.getToggle},
		{"PATCH /api/v1/projects/{name}/toggles/{key}", projectOperate, "toggle.update", s.updateToggle},
		{"DELETE /api/v1/projects/{name}/toggles/{key}", projectOperate, "toggle.delete", s.deleteToggle},
		{"PUT /api/v1/projects/{name}/toggles/{key}/lock", projectOperate, "toggle.lock", s.lockToggle(true)},
		{"DELETE /api/v1/projects/{name}/toggles/{key}/lock", projectOperate, "toggle.unlock", s.lockToggle(false)},
		{"PUT /api/v1/projects/{name}/toggles/{key}/environments/{env}", projectOperate, "toggle.set", s.setToggleState},
		{"GET /api/v1/projects/{name}/toggles/{key}/history", projectRead, "", s.toggleHistory},
		{"GET /api/v1/projects/{name}/toggles/{key}/usage", projectRead, "", s.toggleUsage},
		{"POST /api/v1/projects/{name}/toggles/{key}/evaluate", projectRead, "toggle.evaluate", s.evaluateToggle},
		{"GET /api/v1/projects/{name}/toggle-segments", projectRead, "", s.listToggleSegments},
		{"PUT /api/v1/projects/{name}/toggle-segments/{segment}", projectOperate, "toggle.segment_set", s.setToggleSegment},
		{"DELETE /api/v1/projects/{name}/toggle-segments/{segment}", projectOperate, "toggle.segment_delete", s.deleteToggleSegment},
		{"GET /api/v1/projects/{name}/toggle-safeguards", projectRead, "", s.getToggleSafeguards},
		{"PUT /api/v1/projects/{name}/toggle-safeguards", projectOperate, "toggle.safeguards_set", s.setToggleSafeguards},
		{"GET /api/v1/projects/{name}/toggle-keys", projectOperate, "", s.listToggleKeys},
		{"POST /api/v1/projects/{name}/toggle-keys", projectOperate, "toggle.key_create", s.createToggleKey},
		{"POST /api/v1/projects/{name}/toggle-keys/{id}/rotate", projectOperate, "toggle.key_rotate", s.rotateToggleKey},
		{"DELETE /api/v1/projects/{name}/toggle-keys/{id}", projectOperate, "toggle.key_revoke", s.revokeToggleKey},
	}
}

func (s *Server) toggleService(w http.ResponseWriter) *togglestore.Service {
	if s.Toggles == nil {
		writeErr(w, http.StatusServiceUnavailable, "Switches need schema 47; this server's database does not have it")
		return nil
	}
	return s.Toggles
}

// toggleActor says who and how: through the CLI, the MCP server, the web page
// or a plain API call.
func (s *Server) toggleActor(r *http.Request) togglestore.Actor {
	id := identityFrom(r)
	via := "api"
	switch {
	case r.Context().Value(mcpMarkKey) != nil:
		via = "mcp"
	case strings.HasPrefix(r.UserAgent(), "launchway-cli/"):
		via = "cli"
	case id.Via == "session":
		via = "ui"
	}
	if v := r.Header.Get("X-Launchway-Via"); v == "pipeline" || v == "config" {
		via = v
	}
	return togglestore.Actor{User: id.User, Groups: id.Groups, Via: via}
}

// projectEnvs are the environment names of the engine that allow a project.
func (s *Server) projectEnvs(project string) []string {
	var out []string
	for _, e := range s.Engine.Environments() {
		if e.Allows(project) {
			out = append(out, e.Name)
		}
	}
	return out
}

func whereOf(level, scope, project string) (togglestore.Where, error) {
	if level == "" {
		return togglestore.ProjectWhere(project), nil
	}
	w := togglestore.Where{Level: togglestore.Level(levelOf(level)), Scope: scope}
	if w.Level == togglestore.LevelProject && scope == "" {
		w.Scope = project
	}
	return w, w.Check()
}

// mayEditLevel decides who may define or change a toggle at a level: an
// operator of the project (the route's guard) for the project's own, and an
// administrator of the level for a shared one.
func (s *Server) mayEditLevel(r *http.Request, w togglestore.Where, project string) (int, string) {
	if w.Level == togglestore.LevelProject {
		if w.Scope != project {
			return http.StatusNotFound, fmt.Sprintf("not found: Flight %s", w.Scope)
		}
		return 0, ""
	}
	return s.mayChangeLevel(identityFrom(r), settings.Level(w.Level), w.Scope, false)
}

func (s *Server) listToggles(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	project := r.PathValue("name")
	list, err := svc.List(r.Context(), project, s.projectEnvs(project))
	if err != nil {
		s.fail(w, err)
		return
	}
	pending, err := svc.Pending(r.Context(), project)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "environments": s.projectEnvs(project), "toggles": list, "waiting": pending})
}

func (s *Server) getToggle(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	project := r.PathValue("name")
	sm, err := svc.Get(r.Context(), project, r.PathValue("key"), s.projectEnvs(project))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sm)
}

type toggleBody struct {
	Level         string    `json:"level"`
	Scope         string    `json:"scope"`
	Key           string    `json:"key"`
	Type          string    `json:"type"`
	Kind          *string   `json:"kind"`
	Description   *string   `json:"description"`
	Default       *any      `json:"default"`
	SafeValue     *any      `json:"safe_value"`
	ClientVisible *bool     `json:"client_visible"`
	Owner         *string   `json:"owner"`
	Tags          *[]string `json:"tags"`
	Expires       *string   `json:"expires"`
	Reason        string    `json:"reason"`
}

func parseExpiry(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, fmt.Errorf("%w: expires is a date like 2027-03-01", engine.ErrInvalid)
	}
	return t.UnixMilli(), nil
}

func (s *Server) createToggle(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	var in toggleBody
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	where, err := whereOf(in.Level, in.Scope, project)
	if err != nil {
		s.fail(w, fmt.Errorf("%w: %v", engine.ErrInvalid, err))
		return
	}
	if code, msg := s.mayEditLevel(r, where, project); code != 0 {
		writeErr(w, code, msg)
		return
	}
	def := togglestore.DefinitionInput{Key: togglestore.Key{Where: where, Key: in.Key}, Type: toggles.ValueType(in.Type), Origin: ""}
	if in.Kind != nil {
		def.Kind = toggles.Kind(*in.Kind)
	}
	if in.Description != nil {
		def.Description = *in.Description
	}
	if in.Default != nil {
		def.Default = *in.Default
	} else {
		def.Default = zeroOf(def.Type)
	}
	if in.SafeValue != nil {
		def.SafeValue = *in.SafeValue
	}
	if in.ClientVisible != nil {
		def.ClientVisible = *in.ClientVisible
	}
	if in.Owner != nil {
		def.Owner = *in.Owner
	}
	if in.Tags != nil {
		def.Tags = *in.Tags
	}
	if in.Expires != nil {
		if def.ExpiresAt, err = parseExpiry(*in.Expires); err != nil {
			s.fail(w, err)
			return
		}
	}
	setAudit(r, project+"/"+in.Key, clip(in.Reason, 300))
	d, err := svc.Create(r.Context(), s.toggleActor(r), def, in.Reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

// zeroOf is the default of a toggle that was made with none: off, empty, zero.
func zeroOf(t toggles.ValueType) any {
	switch t {
	case toggles.Boolean:
		return false
	case toggles.String:
		return ""
	case toggles.Number:
		return float64(0)
	}
	return nil
}

// definitionOf finds the toggle a project sees under a key, for the routes
// that change it, and checks that the caller may edit the level it is at.
func (s *Server) definitionOf(w http.ResponseWriter, r *http.Request, svc *togglestore.Service) (*togglestore.Summary, bool) {
	project := r.PathValue("name")
	sm, err := svc.Get(r.Context(), project, r.PathValue("key"), s.projectEnvs(project))
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	if code, msg := s.mayEditLevel(r, sm.Where, project); code != 0 {
		writeErr(w, code, msg)
		return nil, false
	}
	return sm, true
}

func (s *Server) updateToggle(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	sm, ok := s.definitionOf(w, r, svc)
	if !ok {
		return
	}
	var in toggleBody
	if !decode(w, r, &in) {
		return
	}
	p := togglestore.Patch{Description: in.Description, Default: in.Default, SafeValue: in.SafeValue, ClientVisible: in.ClientVisible, Owner: in.Owner, Tags: in.Tags}
	if in.Kind != nil {
		k := toggles.Kind(*in.Kind)
		p.Kind = &k
	}
	if in.Expires != nil {
		exp, err := parseExpiry(*in.Expires)
		if err != nil {
			s.fail(w, err)
			return
		}
		p.ExpiresAt = &exp
	}
	setAudit(r, r.PathValue("name")+"/"+sm.Key.Key, clip(in.Reason, 300))
	d, err := svc.Update(r.Context(), s.toggleActor(r), sm.Key, p, in.Reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) deleteToggle(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	sm, ok := s.definitionOf(w, r, svc)
	if !ok {
		return
	}
	reason := r.URL.Query().Get("reason")
	setAudit(r, r.PathValue("name")+"/"+sm.Key.Key, clip(reason, 300))
	if err := svc.Delete(r.Context(), s.toggleActor(r), sm.Key, reason); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) lockToggle(lock bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc := s.toggleService(w)
		if svc == nil {
			return
		}
		project := r.PathValue("name")
		sm, err := svc.Get(r.Context(), project, r.PathValue("key"), nil)
		if err != nil {
			s.fail(w, err)
			return
		}
		if sm.Level == togglestore.LevelProject {
			writeErr(w, http.StatusBadRequest, "a lock stops nearer levels from defining the toggle: lock one that is shared at a level above its {projects}")
			return
		}
		if code, msg := s.mayChangeLevel(identityFrom(r), settings.Level(sm.Level), sm.Scope, true); code != 0 {
			writeErr(w, code, msg)
			return
		}
		var in toggleBody
		if lock && !decode(w, r, &in) {
			return
		}
		setAudit(r, sm.String(), clip(in.Reason, 300))
		if err := svc.SetLock(r.Context(), s.toggleActor(r), sm.Key, lock, in.Reason); err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"locked": lock})
	}
}

type stateBody struct {
	State  toggles.State `json:"state"`
	Reason string        `json:"reason"`
	// At schedules the change for a time (RFC 3339) instead of making it now.
	At string `json:"at"`
}

func (s *Server) setToggleState(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	sm, ok := s.definitionOf(w, r, svc)
	if !ok {
		return
	}
	project, env := r.PathValue("name"), r.PathValue("env")
	if msg := s.envRefusal(identityFrom(r), project, env); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	var in stateBody
	if !decode(w, r, &in) {
		return
	}
	sc := togglestore.StateChange{Project: project, Toggle: sm.Key, Environment: env, State: in.State, Reason: in.Reason}
	setAudit(r, project+"/"+sm.Key.Key+"/"+env, clip(in.Reason, 300))
	if in.At != "" {
		at, err := time.Parse(time.RFC3339, in.At)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "at is a time like 2026-12-01T09:00:00Z")
			return
		}
		sched, err := svc.ScheduleChange(r.Context(), s.toggleActor(r), sc, at.UnixMilli())
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"scheduled": sched})
		return
	}
	out, err := svc.ChangeState(r.Context(), s.toggleActor(r), sc)
	if err != nil {
		s.fail(w, err)
		return
	}
	code := http.StatusOK
	if out.Waiting {
		code = http.StatusAccepted
	}
	writeJSON(w, code, out)
}

func (s *Server) toggleHistory(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	project := r.PathValue("name")
	sm, err := svc.Get(r.Context(), project, r.PathValue("key"), nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	h, err := svc.History(r.Context(), sm.Key, r.URL.Query().Get("environment"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"toggle": sm.Key.Key, "changes": h})
}

func (s *Server) toggleUsage(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	u, err := svc.Usage(r.Context(), r.PathValue("name"), r.PathValue("key"), days)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"toggle": r.PathValue("key"), "usage": u,
		"privacy": "Counts only: how many evaluations, per Airfield, per day, per value. Nothing about who was evaluated is stored."})
}

// evaluateToggle tries a context against the toggle as it is now, in every
// environment, without counting it: a way to check a rule before relying on it.
func (s *Server) evaluateToggle(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	var in struct {
		Context     toggles.Context `json:"context"`
		Environment string          `json:"environment"`
	}
	if !decode(w, r, &in) {
		return
	}
	project, key := r.PathValue("name"), r.PathValue("key")
	envs := s.projectEnvs(project)
	if in.Environment != "" {
		envs = []string{in.Environment}
	}
	out := map[string]togglestore.Evaluated{}
	for _, env := range envs {
		sn, err := svc.Current(r.Context(), project, env)
		if err != nil {
			s.fail(w, err)
			return
		}
		res := toggles.Evaluate(sn.Ruleset, key, in.Context)
		out[env] = togglestore.Evaluated{Value: res.Value, Reason: res.Reason, RuleID: res.RuleID}
	}
	writeJSON(w, http.StatusOK, map[string]any{"toggle": key, "values": out})
}

func (s *Server) toggleChanges(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	list, err := svc.Pending(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"waiting": list})
}

// changeOfProject finds a change and checks that it belongs to the project in
// the path, and that the caller has the role in its environment.
func (s *Server) changeOfProject(w http.ResponseWriter, r *http.Request, svc *togglestore.Service) (*togglestore.Change, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "a change's id is a number")
		return nil, false
	}
	project := r.PathValue("name")
	ch, err := svc.Change(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	if ch.Project != project && (ch.Project != "" || ch.Level == togglestore.LevelProject) {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("not found: no change %d in {project} %s", id, project))
		return nil, false
	}
	if ch.Project == "" { // a shared toggle's change: the level's administrators
		if code, msg := s.mayChangeLevel(identityFrom(r), settings.Level(ch.Level), ch.Scope, false); code != 0 {
			writeErr(w, code, msg)
			return nil, false
		}
	}
	if ch.Environment != "" {
		if msg := s.envRefusal(identityFrom(r), project, ch.Environment); msg != "" {
			writeErr(w, http.StatusForbidden, msg)
			return nil, false
		}
	}
	return ch, true
}

func (s *Server) revertToggleChange(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	ch, ok := s.changeOfProject(w, r, svc)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, fmt.Sprintf("%s/%s/%s change %d", r.PathValue("name"), ch.Toggle, ch.Environment, ch.ID), clip(in.Reason, 300))
	out, err := svc.Revert(r.Context(), s.toggleActor(r), ch.ID, in.Reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) decideToggleChange(what string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc := s.toggleService(w)
		if svc == nil {
			return
		}
		ch, ok := s.changeOfProject(w, r, svc)
		if !ok {
			return
		}
		var in struct {
			Comment string `json:"comment"`
		}
		if !decode(w, r, &in) {
			return
		}
		setAudit(r, fmt.Sprintf("%s/%s/%s change %d", r.PathValue("name"), ch.Toggle, ch.Environment, ch.ID), clip(in.Comment, 300))
		who := s.toggleActor(r)
		var out any
		var err error
		switch what {
		case "approve":
			out, err = svc.Approve(r.Context(), who, ch.ID, in.Comment)
		case "reject":
			out, err = svc.Reject(r.Context(), who, ch.ID, in.Comment)
		default:
			out, err = svc.Withdraw(r.Context(), who, ch.ID)
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func (s *Server) toggleSchedules(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	list, err := svc.Schedules(r.Context(), r.PathValue("name"), r.URL.Query().Get("toggle"), r.URL.Query().Get("all") == "true")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": list})
}

func (s *Server) cancelToggleSchedule(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "a scheduled change's id is a number")
		return
	}
	setAudit(r, fmt.Sprintf("%s schedule %d", r.PathValue("name"), id), "")
	if err := svc.CancelSchedule(r.Context(), r.PathValue("name"), id); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"canceled": true})
}

func (s *Server) toggleTimeline(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	env := r.URL.Query().Get("environment")
	if env == "" {
		writeErr(w, http.StatusBadRequest, "say which environment: ?environment=production")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := svc.Timeline(r.Context(), r.PathValue("name"), env, since, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"environment": env, "changes": list})
}

// revertTogglesSince goes back over the toggle changes made in an environment
// since a time: {"environment", "since", "reason"}; with "dry_run" it only lists.
func (s *Server) revertTogglesSince(w http.ResponseWriter, r *http.Request) {
	svc := s.toggleService(w)
	if svc == nil {
		return
	}
	var in struct {
		Environment string `json:"environment"`
		Since       int64  `json:"since"`
		Reason      string `json:"reason"`
		DryRun      bool   `json:"dry_run"`
	}
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	if in.Environment == "" || in.Since <= 0 {
		writeErr(w, http.StatusBadRequest, `say the environment and since (a time in Unix milliseconds): {"environment": "production", "since": 1790000000000, "reason": "..."}`)
		return
	}
	if msg := s.envRefusal(identityFrom(r), project, in.Environment); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	if in.DryRun {
		list, err := svc.ChangesSince(r.Context(), project, in.Environment, in.Since)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"would_revert": list})
		return
	}
	setAudit(r, project+"/"+in.Environment, clip(in.Reason, 300))
	res, err := svc.RevertSince(r.Context(), s.toggleActor(r), project, in.Environment, in.Since, in.Reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reverted": res})
}

var _ = slices.Contains[[]string]
var _ = json.Marshal
var _ = auth.Read
