package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Commands on a deployment's rollout: promote, promote-full, abort, retry,
// and withdrawing one no deploy runner has taken. Each needs the operator
// role on the run's project (the route checks it) from a binding that
// covers the deployment's environment (envRefusal). The protected-
// environment rule (the starter may not promote) is the engine's.

// envRefusal says why someone may not act on a deployment to env, or "".
func (s *Server) envRefusal(id *identity, project, env string) string {
	if s.Auth == nil || env == "" {
		return ""
	}
	// What the person has there, never more than a limited token allows.
	if id.limit(s.cur().Policy.RoleOnEnvironment(id.User, id.Groups, project, env), project) >= auth.Operator {
		return ""
	}
	return fmt.Sprintf("you need the operator role on %s in Airfield %s to do this", project, env)
}

// deploymentAccess checks the caller's role on a deployment's environment;
// it writes the refusal and reports false.
func (s *Server) deploymentAccess(w http.ResponseWriter, r *http.Request, depID string) bool {
	runID := r.PathValue("id")
	env, err := s.Engine.EnvironmentOf(runID, "", depID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	project, err := s.Engine.RunProject(runID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if msg := s.envRefusal(identityFrom(r), project, env); msg != "" {
		writeErr(w, http.StatusForbidden, msg)
		return false
	}
	return true
}

// command gives a person's command. The body is optional:
// {"comment": "..."}.
func (s *Server) command(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Comment string `json:"comment"`
		}
		if !decode(w, r, &in) {
			return
		}
		dep := r.PathValue("dep")
		// The entry records the request; what the deploy runner did with it
		// is recorded when it reports (deployment.command_outcome).
		setAudit(r, r.PathValue("id")+"/"+dep, clip("requested: "+in.Comment, 300))
		if !s.deploymentAccess(w, r, dep) {
			return
		}
		if action == "promote-full" && strings.TrimSpace(in.Comment) == "" {
			writeErr(w, http.StatusBadRequest, "promote full skips the remaining steps and checks of the rollout: say why in the comment, which is kept in the Logbook")
			return
		}
		cmd, err := s.Engine.RequestCommand(engine.CommandRequest{Run: r.PathValue("id"), Deployment: dep, Action: action,
			Actor: identityFrom(r).User, Comment: in.Comment})
		if err != nil {
			s.fail(w, err)
			return
		}
		setAudit(r, r.PathValue("id")+"/"+dep+"/"+cmd.ID, "")
		writeJSON(w, http.StatusCreated, cmd)
	}
}

func (s *Server) withdrawCommand(w http.ResponseWriter, r *http.Request) {
	dep := r.PathValue("dep")
	setAudit(r, r.PathValue("id")+"/"+dep+"/"+r.PathValue("cmd"), "")
	if !s.deploymentAccess(w, r, dep) {
		return
	}
	if err := s.Engine.WithdrawCommand(r.PathValue("id"), r.PathValue("cmd"), identityFrom(r).User); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// controls says which commands the caller could give a deployment now,
// and why not the others, in plain words: the UI shows the buttons from
// it. The server checks again when a command is given.
func (s *Server) controls(w http.ResponseWriter, r *http.Request) {
	id := identityFrom(r)
	runID, dep := r.PathValue("id"), r.PathValue("dep")
	env, ctls, err := s.Engine.Controls(runID, dep, id.User)
	if err != nil {
		s.fail(w, err)
		return
	}
	project, err := s.Engine.RunProject(runID)
	if err != nil {
		s.fail(w, err)
		return
	}
	role := ""
	switch {
	case !s.authorize(id, auth.Operate, project):
		role = fmt.Sprintf("you need the operator role on %s to do this", project)
	default:
		role = s.envRefusal(id, project, env.Name)
	}
	for i := range ctls {
		if role != "" {
			ctls[i].Available, ctls[i].Forbidden, ctls[i].Reason = false, true, role
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"environment": env.Name, "protected": env.Protected, "controls": ctls,
		"withdraw": role == "", "withdraw_reason": role})
}

// ---- for deploy runners ----

func (s *Server) deployCommands(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner      engine.RunnerInfo `json:"runner"`
		WaitSeconds int               `json:"wait_seconds"`
	}
	if !decode(w, r, &in) {
		return
	}
	targets, ok := s.deployRunner(w, r, &in.Runner)
	if !ok {
		return
	}
	wait := time.Duration(min(max(in.WaitSeconds, 0), 50)) * time.Second
	ctx, cancel := s.longLived(r)
	defer cancel()
	job, err := s.Engine.TakeCommand(ctx, in.Runner, targets, wait)
	switch {
	case err != nil && ctx.Err() != nil:
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		s.fail(w, err)
		return
	case job == nil:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// A runner that went away meanwhile never sees it; the take runs out
	// and another takes it.
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) deployCommandDone(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Runner engine.RunnerInfo    `json:"runner"`
		Result engine.CommandResult `json:"result"`
	}
	if !decode(w, r, &in) {
		return
	}
	targets, ok := s.deployRunner(w, r, &in.Runner)
	if !ok {
		return
	}
	if err := s.Engine.FinishCommand(in.Runner, targets, in.Result); err != nil {
		s.fail(w, err)
		return
	}
	s.recordCommandOutcome(r, in.Runner, in.Result.Take)
	w.WriteHeader(http.StatusNoContent)
}

// recordCommandOutcome writes what a deploy runner did with a command to
// the Logbook, apart from the entry for the request: ok for a command
// that got to the rollout, failed for one that did not, with why.
func (s *Server) recordCommandOutcome(r *http.Request, runner engine.RunnerInfo, take string) {
	cv, err := s.Engine.CommandByTake(take)
	if err != nil || cv.State != engine.CommandDone {
		return // handed back: the command is still open
	}
	project, _ := s.Engine.RunProject(cv.RunID)
	outcome := "ok"
	if cv.Result != "applied" && cv.Result != "already_applied" {
		outcome = "failed"
	}
	s.record(&engine.AuditEntry{At: s.Now().UnixMilli(), User: "runner:" + nonEmpty(runner.Name, runner.ID), Source: s.clientAddr(r), RequestID: reqID(r),
		Action: "deployment.command_outcome", Project: project, Target: cv.RunID + "/" + cv.Deployment + "/" + cv.ID, Outcome: outcome,
		Detail: cv.Action + " asked for by " + cv.Actor + ": " + cv.Result + ": " + cv.Detail})
}

// presentRun redacts the run for sending, and makes a run's actions say what this person may do: without the
// operator role none is available, and each says why and what would help.
func (s *Server) presentRun(id *identity, d *engine.RunDetail) {
	d.Source = redactSource(d.Source)
	s.presentDeployments(id, d.Deployments)
	if s.authorize(id, auth.Operate, d.Project) {
		return
	}
	for i := range d.Actions {
		d.Actions[i].Available = false
		d.Actions[i].Reason = "you need the operator role on " + d.Project + " to do this"
		d.Actions[i].WouldHelp = "ask an administrator for the operator role on " + d.Project
		d.Actions[i].Instead = ""
	}
}
