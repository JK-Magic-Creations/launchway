package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// Variables at levels. A variable is set at a level (installation,
// organization, unit or project), overridden by the same name at a nearer
// level, and may be locked by the level that set it. The routes name the
// level and scope in the query or the body, so that one set of operations
// serves every level; who may call each is decided here, by the level:
//
//	read           anyone who may see something at that level (see mayReadLevel)
//	set / delete   an administrator of the unit, organization or installation;
//	               an operator of the project
//	lock / unlock  an administrator at that level, of the project at a project
//
// "An administrator of a unit" is an admin of every project in it.

func (s *Server) variableRoutes() []route {
	return []route{
		{"GET /api/v1/variables", signedIn, "", s.listVariables},
		{"PUT /api/v1/variables/{name}", signedIn, "variable.set", s.setVariable},
		{"DELETE /api/v1/variables/{name}", signedIn, "variable.delete", s.deleteVariable},
		{"PUT /api/v1/variables/{name}/lock", signedIn, "variable.lock", s.lockVariable},
		{"DELETE /api/v1/variables/{name}/lock", signedIn, "variable.unlock", s.unlockVariable},
		{"GET /api/v1/projects/{name}/variables", projectRead, "", s.projectVariables},
		{"GET /api/v1/runs/{id}/variables", runRead, "", s.runVariables},
	}
}

func (s *Server) variableService(w http.ResponseWriter) *settings.VariableService {
	if s.Variables == nil {
		writeErr(w, http.StatusServiceUnavailable, "variables at levels need schema 29; this server's database does not have it")
		return nil
	}
	return s.Variables
}

// levelOf reads a level from a request, accepting the American spelling and
// the older "organisation".
func levelOf(s string) settings.Level {
	if s == "organisation" {
		s = "organization"
	}
	return settings.Level(s)
}

// unitProjects returns the projects of a unit.
func (s *Server) unitProjects(unit string) []string {
	if s.Settings == nil {
		return nil
	}
	lay, err := s.Settings.Levels()
	if err != nil {
		return nil
	}
	for _, u := range lay.Units {
		if u.Name == unit {
			return u.Projects
		}
	}
	return nil
}

// mayReadLevel decides who may see the variables at a level.
func (s *Server) mayReadLevel(id *identity, level settings.Level, scope string) (int, string) {
	if level == settings.LevelProject {
		if !s.authorize(id, auth.Read, scope) {
			return http.StatusNotFound, fmt.Errorf("%w: project %s", engine.ErrNotFound, scope).Error()
		}
		return 0, ""
	}
	if !s.authorize(id, auth.ReadAny, "") {
		return http.StatusForbidden, "you have no access to any project"
	}
	return 0, ""
}

// mayChangeLevel decides who may set or delete a variable at a level, or, with
// lock, lock or unlock one.
func (s *Server) mayChangeLevel(id *identity, level settings.Level, scope string, lock bool) (int, string) {
	switch level {
	case settings.LevelInstallation, settings.LevelOrganization:
		if !s.authorize(id, auth.Administer, "") {
			return http.StatusForbidden, "only admins of every project may change variables at this level"
		}
	case settings.LevelUnit:
		projects := s.unitProjects(scope)
		if len(projects) == 0 {
			if !s.authorize(id, auth.Administer, "") {
				return http.StatusForbidden, "only admins of every project may change variables of a unit with no projects"
			}
			return 0, ""
		}
		for _, p := range projects {
			if !s.authorize(id, auth.AdministerProject, p) {
				return http.StatusForbidden, "only an admin of every project in the unit may change its variables"
			}
		}
	case settings.LevelProject:
		if !s.authorize(id, auth.Read, scope) {
			return http.StatusNotFound, fmt.Errorf("%w: project %s", engine.ErrNotFound, scope).Error()
		}
		if lock && !s.authorize(id, auth.AdministerProject, scope) {
			return http.StatusForbidden, fmt.Sprintf("you need the admin role on %s to lock or unlock a variable", scope)
		}
		if !lock && !s.authorize(id, auth.Operate, scope) {
			return http.StatusForbidden, fmt.Sprintf("you need the operator role on %s to change its variables", scope)
		}
	default:
		return http.StatusBadRequest, "level is installation, organization, unit or project"
	}
	return 0, ""
}

// keyOf reads a variable's key from the path and a level, scope and
// environment given as values.
func keyOf(name, level, scope, env string) settings.VarKey {
	return settings.VarKey{Level: levelOf(level), Scope: scope, Name: name, Environment: env}
}

func (s *Server) listVariables(w http.ResponseWriter, r *http.Request) {
	svc := s.variableService(w)
	if svc == nil {
		return
	}
	q := r.URL.Query()
	level := levelOf(q.Get("level"))
	if !level.Valid() {
		writeErr(w, http.StatusBadRequest, "say which level: ?level=installation, organization, unit or project (and &scope= for a unit or a project)")
		return
	}
	if code, msg := s.mayReadLevel(identityFrom(r), level, q.Get("scope")); code != 0 {
		writeErr(w, code, msg)
		return
	}
	var names []string
	if n := q.Get("names"); n != "" {
		names = strings.Split(n, ",")
	}
	v, err := svc.View(r.Context(), level, q.Get("scope"), q.Get("environment"), names)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) projectVariables(w http.ResponseWriter, r *http.Request) {
	svc := s.variableService(w)
	if svc == nil {
		return
	}
	var names []string
	if n := r.URL.Query().Get("names"); n != "" {
		names = strings.Split(n, ",")
	}
	v, err := svc.View(r.Context(), settings.LevelProject, r.PathValue("name"), r.URL.Query().Get("environment"), names)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type variableBody struct {
	Level       string          `json:"level"`
	Scope       string          `json:"scope"`
	Environment string          `json:"environment"`
	Type        string          `json:"type"`
	Of          string          `json:"of"`
	Value       json.RawMessage `json:"value"`
	Description string          `json:"description"`
	Reason      string          `json:"reason"`
}

func (s *Server) setVariable(w http.ResponseWriter, r *http.Request) {
	svc := s.variableService(w)
	if svc == nil {
		return
	}
	var in variableBody
	if !decode(w, r, &in) {
		return
	}
	k := keyOf(r.PathValue("name"), in.Level, in.Scope, in.Environment)
	if code, msg := s.mayChangeLevel(identityFrom(r), k.Level, k.Scope, false); code != 0 {
		writeErr(w, code, msg)
		return
	}
	setAudit(r, k.String(), "")
	res, err := svc.Set(r.Context(), identityFrom(r).User, settings.SetInput{VarKey: k, Type: settings.VarType(in.Type), Of: in.Of, Value: in.Value, Description: in.Description})
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) deleteVariable(w http.ResponseWriter, r *http.Request) {
	svc := s.variableService(w)
	if svc == nil {
		return
	}
	q := r.URL.Query()
	k := keyOf(r.PathValue("name"), q.Get("level"), q.Get("scope"), q.Get("environment"))
	if code, msg := s.mayChangeLevel(identityFrom(r), k.Level, k.Scope, false); code != 0 {
		writeErr(w, code, msg)
		return
	}
	setAudit(r, k.String(), "")
	if err := svc.Delete(r.Context(), identityFrom(r).User, k); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) lockVariable(w http.ResponseWriter, r *http.Request) {
	svc := s.variableService(w)
	if svc == nil {
		return
	}
	var in variableBody
	if !decode(w, r, &in) {
		return
	}
	k := keyOf(r.PathValue("name"), in.Level, in.Scope, in.Environment)
	if code, msg := s.mayChangeLevel(identityFrom(r), k.Level, k.Scope, true); code != 0 {
		writeErr(w, code, msg)
		return
	}
	setAudit(r, k.String(), in.Reason)
	res, err := svc.Lock(r.Context(), identityFrom(r).User, k, in.Reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) unlockVariable(w http.ResponseWriter, r *http.Request) {
	svc := s.variableService(w)
	if svc == nil {
		return
	}
	q := r.URL.Query()
	k := keyOf(r.PathValue("name"), q.Get("level"), q.Get("scope"), q.Get("environment"))
	if code, msg := s.mayChangeLevel(identityFrom(r), k.Level, k.Scope, true); code != 0 {
		writeErr(w, code, msg)
		return
	}
	setAudit(r, k.String(), "")
	v, err := svc.Unlock(r.Context(), identityFrom(r).User, k)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"variable": v})
}

// runVariables are the variables a run used, as recorded when it was made.
func (s *Server) runVariables(w http.ResponseWriter, r *http.Request) {
	svc := s.variableService(w)
	if svc == nil {
		return
	}
	id := r.PathValue("id")
	d, err := s.Engine.GetRun(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	vars, err := svc.RunVariables(r.Context(), id, d.RerunOf)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": id, "rerun_of": d.RerunOf, "variables": vars})
}
