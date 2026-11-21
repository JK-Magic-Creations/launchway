package api

import (
	"fmt"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Rollback after a successful deployment (schema 32; see
// engine/rollbacks.go). The server says, in the rollback action of a
// deployment, an application's unit or an environment, whether a person may
// roll back and to what; asking for it is refused with the same words when
// it is not.

func (s *Server) rollbackRoutes() []route {
	return []route{
		{"GET /api/v1/projects/{name}/rollback", projectRead, "", s.rollbackOptions},
		{"POST /api/v1/projects/{name}/rollbacks", projectOperate, "deployment.rollback", s.requestRollback},
		{"GET /api/v1/rollbacks/review", signedIn, "", s.rollbacksToReview},
		{"POST /api/v1/runs/{id}/review", runOperate, "rollback.review", s.reviewRollback},
	}
}

// roleAction makes a rollback action say what this person may do: with
// too little role it is unavailable, saying so and what would help, and
// offers no targets.
func (s *Server) roleAction(id *identity, project, env string, a *engine.Action) {
	why := ""
	switch {
	case !s.authorize(id, auth.Operate, project):
		why = "you need the operator role on " + project + " to do this"
	default:
		why = s.envRefusal(id, project, env)
	}
	if why == "" {
		return
	}
	a.Available, a.Queueable, a.Targets, a.Instead = false, false, nil, ""
	a.Reason = why
	a.WouldHelp = "ask an administrator for the operator role on " + project
	if env != "" {
		a.WouldHelp += " in environment " + env
	}
	a.Risk, a.Suggestion = "", ""
}

// presentDeployments applies the person's role to the actions of
// deployments.
func (s *Server) presentDeployments(id *identity, list []engine.DeploymentView) {
	for i := range list {
		for j := range list[i].Actions {
			s.roleAction(id, list[i].Project, list[i].Environment, &list[i].Actions[j])
		}
	}
}

func (s *Server) rollbackOptions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	env := q.Get("environment")
	if env == "" {
		writeErr(w, http.StatusBadRequest, "say which environment: ?environment=dev")
		return
	}
	plan, err := s.Engine.RollbackPlan(r.PathValue("name"), env, q.Get("service"))
	if err != nil {
		s.fail(w, err)
		return
	}
	s.roleAction(identityFrom(r), plan.Project, env, &plan.Action)
	writeJSON(w, http.StatusOK, plan)
}

// requestRollback asks for a rollback: {"environment", "service", "to",
// "reason", "queue"}. It answers 201 with the rollback run.
func (s *Server) requestRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Environment string `json:"environment"`
		Service     string `json:"service"`
		To          string `json:"to"`
		Reason      string `json:"reason"`
		Queue       bool   `json:"queue"`
	}
	if !decode(w, r, &in) {
		return
	}
	project := r.PathValue("name")
	if in.Environment == "" {
		writeErr(w, http.StatusBadRequest, "say which environment to roll back: {\"environment\": \"dev\", \"reason\": \"...\"}")
		return
	}
	setAudit(r, fmt.Sprintf("%s/%s", project, in.Environment), clip("requested: "+in.Reason, 300))
	id := identityFrom(r)
	if msg := s.envRefusal(id, project, in.Environment); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	sum, err := s.Engine.RequestRollback(engine.RollbackRequest{Project: project, Environment: in.Environment, Service: in.Service,
		To: in.To, Reason: in.Reason, Actor: id.User, Queue: in.Queue})
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, fmt.Sprintf("%s/%s", project, in.Environment), clip(fmt.Sprintf("run #%d %s: %s", sum.Number, sum.ID, sum.Message), 400))
	writeJSON(w, http.StatusCreated, sum)
}

// rollbacksToReview lists the emergency rollbacks waiting for an approver's
// acknowledgment, each with whether the caller may give it.
func (s *Server) rollbacksToReview(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	list, err := s.Engine.RollbacksNeedingReview(id.User, id.Groups, s.visibleFilter(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	for i := range list {
		it := &list[i]
		if it.Acknowledge.Available {
			s.roleAction(id, it.Project, it.Environment, &it.Acknowledge)
		}
	}
	writeJSON(w, http.StatusOK, list)
}

// reviewRollback acknowledges an emergency rollback: {"comment": "..."}.
func (s *Server) reviewRollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Comment string `json:"comment"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, r.PathValue("id"), clip("review: "+in.Comment, 300))
	id := identityFrom(r)
	project, err := s.Engine.RunProject(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if env, err := s.Engine.EnvironmentOfRollback(r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	} else if msg := s.envRefusal(id, project, env); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return
	}
	if err := s.Engine.AcknowledgeRollback(r.PathValue("id"), id.User, id.Groups, in.Comment); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
