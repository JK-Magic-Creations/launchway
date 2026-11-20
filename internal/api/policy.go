package api

// Delivery policies (schema 16): the policies in force, a run's policy
// decisions, and waivers. Policies are the server's configuration, so they
// are read here and never changed. Waiving a rule needs the role
// policy.waivers.allow names (admin by default) on the project and, for a
// deploy rule, on the environment (every environment, for a waiver that
// names none); a limited API token never grants more than its limits.

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// waiveRole is the role needed to waive a policy's rule.
func (s *Server) waiveRole() auth.Role {
	if s.WaiveRole == nil {
		return auth.Admin
	}
	return *s.WaiveRole
}

// waiveRefusal says why someone may not waive rules of a project (and, for
// a deploy rule, of an environment: env "" means every environment), or "".
func (s *Server) waiveRefusal(id *identity, project, env string, deployRule bool) string {
	need := s.waiveRole()
	if need == auth.NoRole {
		return "waivers are turned off (policy.waivers.allow: none)"
	}
	if s.Auth == nil {
		return ""
	}
	if id == nil || id.Via == viaNone || id.User == "" {
		return "sign in first"
	}
	role := s.roleOn(id, project)
	if role < need {
		return fmt.Sprintf("you need the %s role on %s to waive its policies (policy.waivers.allow)", need, project)
	}
	if deployRule {
		// RoleOnEnvironment with no environment counts only the bindings
		// that are not limited to environments.
		if id.limit(s.Auth.Policy.RoleOnEnvironment(id.User, id.Groups, project, env), project) < need {
			if env == "" {
				return fmt.Sprintf("a deploy rule waived for every environment needs the %s role on %s from a binding not limited to environments; name the environment", need, project)
			}
			return fmt.Sprintf("you need the %s role on %s in environment %s to waive its deploy rules", need, project, env)
		}
	}
	return ""
}

// listPolicies shows the policies in force, their sources and recent
// verdicts of the runs the caller may see.
func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	only, err := s.visibleProjects(identityFrom(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	n, err := intParam(r, "recent")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	o, err := s.Engine.PolicyOverview(only, min(n, 100))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// runPolicy shows a run's policy decisions, the waivers that could apply to
// it, and whether the caller may waive.
func (s *Server) runPolicy(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	ds, err := s.Engine.RunPolicy(runID)
	if err != nil {
		s.fail(w, err)
		return
	}
	project, err := s.Engine.RunProject(runID)
	if err != nil {
		s.fail(w, err)
		return
	}
	ws, err := s.Engine.Waivers(engine.WaiverFilter{Project: project, Run: runID})
	if err != nil {
		s.fail(w, err)
		return
	}
	refusal := s.waiveRefusal(identityFrom(r), project, "", false)
	writeJSON(w, http.StatusOK, map[string]any{"decisions": ds, "waivers": ws, "can_waive": refusal == "", "waive_refusal": refusal,
		"rules": s.Engine.PolicyNames(), "catalogue": policy.Catalogue, "max_waiver_days": s.Engine.MaxWaiverDays()})
}

// listWaivers lists waivers of the projects the caller may see:
// ?project=, ?active=1, ?limit=.
func (s *Server) listWaivers(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	only, err := s.visibleProjects(id)
	if err != nil {
		s.fail(w, err)
		return
	}
	n, err := intParam(r, "limit")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	project := r.URL.Query().Get("project")
	if project != "" && !s.authorize(id, auth.Read, project) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%w: project %s", engine.ErrNotFound, project).Error())
		return
	}
	ws, err := s.Engine.Waivers(engine.WaiverFilter{Project: project, Active: r.URL.Query().Get("active") == "1", Projects: only, Limit: n})
	if err != nil {
		s.fail(w, err)
		return
	}
	// The projects the caller may waive rules of, for the UI's form.
	known, err := s.knownProjects()
	if err != nil {
		s.fail(w, err)
		return
	}
	may := []string{}
	for _, p := range known {
		if s.authorize(id, auth.Read, p) && s.waiveRefusal(id, p, "", false) == "" {
			may = append(may, p)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"waivers": ws, "waive_projects": may, "rules": s.Engine.PolicyNames()})
}

// grantWaiver waives a rule: {"policy", "rule", "reason", "expires_in_hours"
// or "expires_at" (Unix ms), and optionally "run", "environment", "finding"}.
func (s *Server) grantWaiver(w http.ResponseWriter, r *http.Request) {
	var in struct {
		engine.WaiverRequest
		ExpiresInHours int `json:"expires_in_hours"`
	}
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	id := identityFrom(r)
	rq := in.WaiverRequest
	rq.Project, rq.Actor = project, id.User
	if in.ExpiresInHours > 0 {
		rq.ExpiresIn = time.Duration(in.ExpiresInHours) * time.Hour
	}
	target := rq.Policy + "/" + rq.Rule
	if rq.Run != "" {
		target += " run " + rq.Run
	}
	if rq.Environment != "" {
		target += " in " + rq.Environment
	}
	if rq.Finding != "" {
		target += " finding " + rq.Finding
	}
	setAudit(r, target, clip(rq.Reason, 300))
	if msg := s.waiveRefusal(id, project, rq.Environment, policy.PointOf(rq.Rule) == policy.PointDeploy); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	v, err := s.Engine.GrantWaiver(rq)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, v.ID+" "+target, clip(rq.Reason, 300)+"; until "+time.UnixMilli(v.ExpiresAt).UTC().Format(time.RFC3339))
	writeJSON(w, http.StatusCreated, v)
}

// revokeWaiver ends a waiver: {"reason": "..."} is optional.
func (s *Server) revokeWaiver(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &in) {
		return
	}
	project, wid := r.PathValue("name"), r.PathValue("waiver")
	setAudit(r, wid, clip(in.Reason, 300))
	id := identityFrom(r)
	if msg := s.waiveRefusal(id, project, "", false); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	ws, err := s.Engine.Waivers(engine.WaiverFilter{ID: wid, Project: project})
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(ws) == 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("%w: waiver %s", engine.ErrNotFound, wid).Error())
		return
	}
	if msg := s.waiveRefusal(id, project, ws[0].Environment, policy.PointOf(ws[0].Rule) == policy.PointDeploy); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	v, err := s.Engine.RevokeWaiver(project, wid, id.User, in.Reason)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, wid+" "+v.Policy+"/"+v.Rule, "")
	writeJSON(w, http.StatusOK, v)
}
