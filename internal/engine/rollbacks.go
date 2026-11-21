package engine

// Rollback after a successful deployment (schema 32).
//
// A rollback after success is a run of its own: a run of event rollback in
// the same project, asked for by a person with a required reason, holding
// one rollback step that restores an earlier deployment's version of an
// environment's service. The original run is never touched; it only gains
// the link "rolled back later by run #N". The step takes the environment's
// lock like any deployment, is leased to the deploy runner that serves the
// target as a revert action, is watched until the application is healthy,
// and records a deployment of its own (mode rollback) whose previous is what
// ran before.
//
// How a version is restored: Git stays the truth. Rolling back to version V
// reverts, newest first, the commit of every deployment after V that is
// still in effect for the service, one revert commit each (carrying that
// deployment's trailers), pushed together and all or nothing. The other way
// to do it, writing V's values over whatever the files hold, would also
// destroy any later change that was not a deployment (someone's hand edit, a
// scaling change), and leave no record of what was undone; a revert keeps the
// history and refuses, plainly, when a commit cannot be reverted cleanly.
//
// What is still in effect (inEffect) is worked out from the unit's
// deployments: a successful deployment is in effect until a rollback run
// restores something before it. Deployments a pipeline's own rollback step
// reverted, deployments that never succeeded, and those undone by an earlier
// rollback are not: their commits are already undone in Git.
//
// A rollback never restores a database or configuration. Stable is the
// extension point for the future "stable state" (a version with the toggles
// and configuration that ran with it); nothing sets it yet.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Reasons are never longer than this.
const maxReasonLen = 1000

// Names of the steps of a rollback run.
const (
	StepRollback        = "rollback"
	StepRollbackApprove = "approve"
)

// How an environment treats an emergency rollback.
const (
	EmergencyAllow           = "allow"            // no approval first; an approver reviews it afterwards (the default)
	EmergencyRequireApproval = "require_approval" // an approval step comes first, even for an emergency
)

// RollbackRequest asks for a rollback.
type RollbackRequest struct {
	Project     string
	Environment string
	Service     string // "": the project's name
	// To is the deployment whose version is restored; "" is the previous
	// successful one.
	To     string
	Reason string
	Actor  string
	// Queue says to wait behind a deployment that holds the environment's
	// lock, rather than be refused.
	Queue bool
}

// unitDep is one deployment of a service as the rollback logic sees it.
type unitDep struct {
	ID, RunID                                   string
	RunNumber                                   int
	Version, Mode, Repo, Branch, Commit, Revert string
	Verdict                                     string
	Changed                                     bool
	OpenedAt, DeployedAt, ClosedAt              int64
	RestoreTo                                   string // for a rollback deployment, the deployment it restored
}

func unitHistory(q querier, project, env, service string) ([]unitDep, error) {
	rows, err := q.Query(`SELECT d.id, d.run_id, d.run_number, d.version, d.mode, d.repo, d.branch, d.commit_sha, d.revert_sha, d.verdict,
			d.changed, d.opened_at, d.deployed_at, d.closed_at, COALESCE(r.to_deployment, '')
		FROM deployments d LEFT JOIN rollbacks r ON r.run_id = d.run_id AND d.mode = 'rollback'
		WHERE d.project = ? AND d.environment = ? AND d.service = ? ORDER BY d.opened_at, d.id`, project, env, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []unitDep
	for rows.Next() {
		var d unitDep
		var changed int
		if err := rows.Scan(&d.ID, &d.RunID, &d.RunNumber, &d.Version, &d.Mode, &d.Repo, &d.Branch, &d.Commit, &d.Revert, &d.Verdict,
			&changed, &d.OpenedAt, &d.DeployedAt, &d.ClosedAt, &d.RestoreTo); err != nil {
			return nil, err
		}
		d.Changed = changed != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// inEffect returns the deployments whose version is part of what the service
// runs, oldest first: the last is what runs now. failed is set when the
// newest deployment is a rollback that failed, whose state in Git is not
// known.
func inEffect(h []unitDep) (stack []unitDep, failed *unitDep) {
	for i := range h {
		d := h[i]
		if i == len(h)-1 && d.Mode == "rollback" && d.Verdict == VerdictRollbackFailed {
			failed = &h[i]
		}
		if d.DeployedAt == 0 {
			continue // never succeeded
		}
		if d.Mode == "rollback" {
			for j, s := range stack {
				if s.ID == d.RestoreTo {
					stack = stack[:j+1]
					break
				}
			}
			continue
		}
		if d.Verdict == VerdictRolledBack {
			continue // its own pipeline reverted it
		}
		stack = append(stack, d)
	}
	return stack, failed
}

// RollbackRevertView is a deployment a rollback undoes.
type RollbackRevertView struct {
	Deployment string `json:"deployment"`
	Run        string `json:"run"`
	RunNumber  int    `json:"run_number"`
	Version    string `json:"version"`
	At         int64  `json:"at"`
}

// RollbackTarget is an earlier deployment a service can be rolled back to.
type RollbackTarget struct {
	Deployment string `json:"deployment"`
	Run        string `json:"run"`
	RunNumber  int    `json:"run_number"`
	Version    string `json:"version"`
	At         int64  `json:"at"` // when it was deployed (Unix ms)
	// Previous marks the previous successful deployment: the default.
	Previous bool `json:"previous,omitempty"`
	// Reverts are the deployments that stop running, newest first.
	Reverts []RollbackRevertView `json:"reverts"`
	// Consequence says in words what choosing it does; Suggestions what to
	// check first. Neither blocks the action.
	Consequence string   `json:"consequence"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// RollbackPlan is what can be done about rolling back one service of an
// environment, and why not when nothing can.
type RollbackPlan struct {
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Protected   bool   `json:"protected"`
	// Current is what runs now, as far as Launchway knows.
	Current *DeployRef `json:"current,omitempty"`
	// Holder is the deployment holding the environment's lock, if any.
	Holder *DeployRef `json:"holder,omitempty"`
	// Action is the rollback action as the server reports it: the one list
	// every interface reads. Its Targets are only filled when it is, or
	// could be by queueing, available.
	Action Action `json:"action"`
}

// emergencyPolicy is what applies to an emergency rollback of an
// environment: its own setting, else the installation's, else allow.
func (e *Engine) emergencyPolicy(env Environment) string {
	if env.EmergencyRollback != "" {
		return env.EmergencyRollback
	}
	if p := e.live().emergency; p != "" {
		return p
	}
	return EmergencyAllow
}

// RollbackPlan works out whether, and to what, a service can be rolled
// back. The person's role is not part of it: the API adds that.
func (e *Engine) RollbackPlan(project, environment, service string) (*RollbackPlan, error) {
	plan, _, _, err := e.rollbackPlan(project, environment, service)
	return plan, err
}

// rollbackPlan is RollbackPlan with the unit's history and what of it is in
// effect.
func (e *Engine) rollbackPlan(project, environment, service string) (*RollbackPlan, []unitDep, []unitDep, error) {
	if !e.knows(schemaRollback) {
		return nil, nil, nil, fmt.Errorf("%w: this server does not know rollback runs (schema %d)", ErrConflict, schemaRollback)
	}
	if service == "" {
		service = project
	}
	plan := &RollbackPlan{Project: project, Environment: environment, Service: service}
	act := Action{Name: "rollback", Who: "operator"}
	plan.Action = act
	env, ok := e.live().envs[environment]
	switch {
	case !ok:
		return nil, nil, nil, fmt.Errorf("%w: environment %s", ErrNotFound, environment)
	case !env.Allows(project):
		return nil, nil, nil, fmt.Errorf("%w: project %s does not deploy to %s", ErrNotFound, project, environment)
	}
	plan.Protected = env.Protected
	hist, err := unitHistory(e.db, project, environment, service)
	if err != nil {
		return nil, nil, nil, err
	}
	stack, failed := inEffect(hist)
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		plan.Current = &DeployRef{Deployment: top.ID, Run: top.RunID, Number: top.RunNumber, Version: top.Version}
	}
	no := func(reason, instead, help string) (*RollbackPlan, []unitDep, []unitDep, error) {
		plan.Action.Available, plan.Action.Reason, plan.Action.Instead, plan.Action.WouldHelp = false, reason, instead, help
		return plan, hist, stack, nil
	}
	switch {
	case failed != nil:
		return no(fmt.Sprintf("the last rollback of %s in %s (run #%d) failed, so what Git holds is not known", service, environment, failed.RunNumber),
			"", fmt.Sprintf("look at run #%d, fix the GitOps repository by hand or deploy the version you want with a normal run", failed.RunNumber))
	case len(stack) < 2:
		return no(fmt.Sprintf("there is no earlier successful deployment of %s in %s to roll back to", service, environment),
			"", "deploy a version first; once an earlier one has succeeded it can be restored")
	}
	if on, err := e.targetServed(env.Target); err != nil {
		return nil, nil, nil, err
	} else if !on {
		return no(fmt.Sprintf("no deploy runner is serving target %s right now, so nothing could revert the change and watch the result", env.Target),
			"", fmt.Sprintf("start or repair the deploy runner for target %s (see Runners); it must report in before a rollback is offered", env.Target))
	}
	if why, err := e.cannotWrite(stack[len(stack)-1]); err != nil {
		return nil, nil, nil, err
	} else if why != "" {
		return no(why, "", "give the deploy runner write access to the GitOps repository, then it can revert here")
	}
	// Targets, newest first.
	cur := stack[len(stack)-1]
	for i := len(stack) - 2; i >= 0; i-- {
		t := stack[i]
		tg := RollbackTarget{Deployment: t.ID, Run: t.RunID, RunNumber: t.RunNumber, Version: t.Version, At: t.DeployedAt, Previous: i == len(stack)-2,
			Reverts: []RollbackRevertView{}}
		for j := len(stack) - 1; j > i; j-- {
			r := stack[j]
			tg.Reverts = append(tg.Reverts, RollbackRevertView{Deployment: r.ID, Run: r.RunID, RunNumber: r.RunNumber, Version: r.Version, At: r.DeployedAt})
		}
		tg.Consequence = rollbackConsequence(environment, service, cur, t, stack[i+1:])
		tg.Suggestions = rollbackSuggestions(cur, t, env.Protected)
		plan.Action.Targets = append(plan.Action.Targets, tg)
	}
	if def := plan.Action.Targets[0]; true {
		plan.Action.Risk, plan.Action.Suggestion = def.Consequence, strings.Join(def.Suggestions, " ")
	}
	plan.Action.Emergency = env.Protected
	plan.Action.RequiresApproval = env.Protected && e.emergencyPolicy(env) == EmergencyRequireApproval
	holder, err := e.unitHolder(project, environment, service)
	if err != nil {
		return nil, nil, nil, err
	}
	if holder != nil {
		plan.Holder = holder
		plan.Action.Queueable = true
		plan.Action.Available = false
		plan.Action.Reason = fmt.Sprintf("run #%d is deploying %s to %s right now and holds its lock, so a rollback cannot start until it ends",
			holder.Number, service, environment)
		plan.Action.Instead = fmt.Sprintf("queue the rollback behind run #%d (it starts when run #%d ends, ahead of other waiting deployments), or cancel run #%d first",
			holder.Number, holder.Number, holder.Number)
		return plan, hist, stack, nil
	}
	plan.Action.Available = true
	return plan, hist, stack, nil
}

func (e *Engine) unitHolder(project, env, service string) (*DeployRef, error) {
	var r DeployRef
	err := e.db.QueryRow(`SELECT l.deployment, l.run_id, l.run_number, l.step_id, COALESCE(d.version, '') FROM deploy_locks l
		LEFT JOIN deployments d ON d.id = l.deployment WHERE l.environment = ? AND l.project = ? AND l.service = ?`,
		env, project, service).Scan(&r.Deployment, &r.Run, &r.Number, &r.Step, &r.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// targetServed reports whether a deploy runner holds the observer lease of
// the target: it serves it and has reported in recently.
func (e *Engine) targetServed(target string) (bool, error) {
	var until int64
	err := e.db.QueryRow(`SELECT expires_at FROM target_observers WHERE target = ?`, target).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && until >= e.now().UnixMilli(), err
}

// cannotWrite says why the deploy runner cannot write the GitOps repository,
// from its latest self-check (carried in what it observed of the
// deployment), or "" when it may or has not said otherwise.
func (e *Engine) cannotWrite(cur unitDep) (string, error) {
	check := func(snap string, at int64) string {
		if snap == "" || at == 0 || e.now().UnixMilli()-at > rightsFresh {
			return ""
		}
		var s deploy.Snapshot
		if json.Unmarshal([]byte(snap), &s) != nil || s.Rights == nil || s.Rights.Write == nil || *s.Rights.Write {
			return ""
		}
		return nonEmpty(s.Rights.WriteWhy, "the deploy runner reports that it may not write the GitOps repository")
	}
	var snap string
	var at int64
	err := e.db.QueryRow(`SELECT snapshot, observed_at FROM deployments WHERE id = ?`, cur.ID).Scan(&snap, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if why := check(snap, at); why != "" {
		return why, nil
	}
	if e.knows(schemaControl) {
		err = e.db.QueryRow(`SELECT snapshot, observed_at FROM environment_state WHERE current_deployment = ?`, cur.ID).Scan(&snap, &at)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		return check(snap, at), nil
	}
	return "", nil
}

func runsOf(ds []unitDep) string {
	nums := make([]string, len(ds))
	for i, d := range ds {
		nums[i] = fmt.Sprintf("#%d", d.RunNumber)
	}
	return strings.Join(nums, ", ")
}

// rollbackConsequence says what rolling back to target does, in words.
func rollbackConsequence(env, service string, cur, target unitDep, after []unitDep) string {
	changed := 0
	for _, d := range after {
		if d.Changed {
			changed++
		}
	}
	var what string
	switch {
	case len(after) == 1:
		what = fmt.Sprintf("the deployment of %s (run %s) is reverted in Git", cur.Version, runsOf(after))
	default:
		what = fmt.Sprintf("the %d deployments after it (runs %s) are reverted in Git, newest first", len(after), runsOf(reverse(after)))
	}
	if changed == 0 {
		what = "no commit is made in Git, because the deployments after it changed nothing there"
	}
	return fmt.Sprintf("%s goes from %s back to %s; %s; the database and configuration are not rolled back", env, cur.Version, target.Version, what)
}

func reverse(ds []unitDep) []unitDep {
	out := make([]unitDep, len(ds))
	for i, d := range ds {
		out[len(ds)-1-i] = d
	}
	return out
}

// rollbackSuggestions are what to check before going ahead.
func rollbackSuggestions(cur, target unitDep, protected bool) []string {
	out := []string{fmt.Sprintf("If %s included a database migration, check that it is backward compatible with %s: a rollback never undoes a database.",
		cur.Version, target.Version)}
	if protected {
		out = append(out, "This environment is protected: the rollback goes ahead without an approval first, and an approver reviews it afterwards.")
	}
	return out
}

// pickTarget finds the target a request names (or the default).
func pickTarget(plan *RollbackPlan, to string) (*RollbackTarget, error) {
	if to == "" {
		return &plan.Action.Targets[0], nil
	}
	var names []string
	var byVersion []*RollbackTarget
	for i := range plan.Action.Targets {
		t := &plan.Action.Targets[i]
		if t.Deployment == to {
			return t, nil
		}
		if t.Version == to {
			byVersion = append(byVersion, t)
		}
		names = append(names, fmt.Sprintf("%s (deployment %s, run #%d)", t.Version, t.Deployment, t.RunNumber))
	}
	// A version names its deployment when it is unambiguous.
	switch len(byVersion) {
	case 1:
		return byVersion[0], nil
	case 0:
	default:
		return nil, fmt.Errorf("%w: %s was deployed %d times; name the deployment: %s", ErrConflict, to, len(byVersion), strings.Join(names, ", "))
	}
	return nil, fmt.Errorf("%w: %s is not an earlier successful deployment of %s in %s that is still in effect; the choices are: %s",
		ErrConflict, to, plan.Service, plan.Environment, strings.Join(names, ", "))
}

// RequestRollback creates the rollback run. It refuses, with the same
// words as the plan's action, whatever the plan says cannot be done.
func (e *Engine) RequestRollback(rq RollbackRequest) (*RunSummary, error) {
	rq.Reason = strings.TrimSpace(rq.Reason)
	switch {
	case rq.Reason == "":
		return nil, fmt.Errorf("%w: say why: a rollback needs a reason, which is recorded with it", ErrInvalid)
	case len(rq.Reason) > maxReasonLen:
		return nil, fmt.Errorf("%w: the reason is longer than %d characters", ErrInvalid, maxReasonLen)
	}
	if rq.Service == "" {
		rq.Service = rq.Project
	}
	plan, err := e.RollbackPlan(rq.Project, rq.Environment, rq.Service)
	if err != nil {
		return nil, err
	}
	if !plan.Action.Available && !(plan.Action.Queueable && rq.Queue) {
		return nil, fmt.Errorf("%w: %s", ErrConflict, plan.Action.Reason)
	}
	target, err := pickTarget(plan, rq.To)
	if err != nil {
		return nil, err
	}
	env := e.live().envs[rq.Environment]
	deployDef, src, err := e.deployStepOf(target.Run, target.Deployment)
	if err != nil {
		return nil, err
	}
	r := &pipeline.Restore{
		To:     pipeline.RestoreTo{Deployment: target.Deployment, Version: target.Version, Run: target.Run, RunNumber: target.RunNumber, At: target.At},
		From:   pipeline.RestoreFrom{Deployment: plan.Current.Deployment, Version: plan.Current.Version, Run: plan.Current.Run, RunNumber: plan.Current.Number},
		Reason: rq.Reason, Actor: rq.Actor,
	}
	hist, err := unitHistory(e.db, rq.Project, rq.Environment, rq.Service)
	if err != nil {
		return nil, err
	}
	byID := map[string]unitDep{}
	for _, d := range hist {
		byID[d.ID] = d
	}
	for _, rv := range target.Reverts {
		if d := byID[rv.Deployment]; d.Changed && d.Commit != "" {
			r.Reverts = append(r.Reverts, pipeline.RestoreRevert{Deployment: d.ID, Version: d.Version, Commit: d.Commit, Run: d.RunID, RunNumber: d.RunNumber})
		}
	}
	needsApproval := false
	if env.Protected {
		r.Emergency = true
		if e.emergencyPolicy(env) == EmergencyRequireApproval {
			needsApproval = true
		} else {
			r.ReviewRequired = true
		}
	}
	argo := *deployDef.ArgoCD
	step := &pipeline.Step{ID: StepRollback, Kind: pipeline.KindRollback, Description: "Restore " + target.Version,
		Environment: rq.Environment, Service: rq.Service, ArgoCD: &argo, Rollout: deployDef.Rollout,
		Timeout: pipeline.Duration(pipeline.DefaultRollbackTimeout), Restore: r}
	steps := []*pipeline.Step{step}
	if needsApproval {
		step.Needs = []string{StepRollbackApprove}
		ap := &pipeline.Step{ID: StepRollbackApprove, Kind: pipeline.KindApproval,
			Message:   fmt.Sprintf("Roll %s in %s back from %s to %s? %s", rq.Service, rq.Environment, plan.Current.Version, target.Version, rq.Reason),
			Timeout:   pipeline.Duration(pipeline.DefaultApprovalTimeout),
			Approvers: &pipeline.Approvers{Count: 1}}
		if env.Reviewers != nil {
			ap.Approvers = &pipeline.Approvers{Groups: env.Reviewers.Groups, Users: env.Reviewers.Users, Count: 1}
		}
		steps = []*pipeline.Step{ap, step}
	}
	p := &pipeline.Pipeline{Name: "rollback", Steps: steps}
	return e.CreateRun(NewRun{Project: rq.Project, Pipeline: p, Source: src,
		Trigger: Trigger{Event: pipeline.EventRollback, Actor: rq.Actor, Message: RollbackHeadline(r, rq.Project, rq.Environment, rq.Service)}})
}

// RollbackHeadline is the sentence that says what a rollback run is, the
// same in every interface: "Emergency rollback of dev: v1.4.0 → v1.3.2, by
// ann: the reason".
func RollbackHeadline(r *pipeline.Restore, project, env, service string) string {
	what := "Rollback"
	if r.Emergency {
		what = "Emergency rollback"
	}
	of := env
	if service != "" && service != project {
		of = service + " in " + env
	}
	return fmt.Sprintf("%s of %s: %s → %s, by %s: %s", what, of, r.From.Version, r.To.Version, r.Actor, r.Reason)
}

// deployStepOf reads the deploy step that made a deployment, from its run,
// and the source that run deployed from.
func (e *Engine) deployStepOf(runID, depID string) (*pipeline.Step, Source, error) {
	evs, _, err := loadEvents(e.db, runID)
	if err != nil {
		return nil, Source{}, err
	}
	st := e.fold(evs)
	if st == nil {
		return nil, Source{}, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err := needsNewer(st); err != nil {
		return nil, Source{}, err
	}
	id, _ := st.deploymentStep(depID)
	def := st.Pipeline.Step(id)
	if def == nil || def.ArgoCD == nil || def.ArgoCD.WriteBack == nil {
		return nil, Source{}, fmt.Errorf("%w: the run that made deployment %s has no record of how it was deployed, so it cannot be restored", ErrConflict, depID)
	}
	return def, st.Source, nil
}

// restoreStep returns the run's rollback step, or nil if it is not a
// rollback run.
func (st *RunState) restoreStep() *pipeline.Step {
	if st.Pipeline == nil {
		return nil
	}
	for _, s := range st.Pipeline.Steps {
		if s.IsRestore() {
			return s
		}
	}
	return nil
}

// isRollbackRun reports whether the run is a rollback run.
func (st *RunState) isRollbackRun() bool { return st.restoreStep() != nil }

// rollbackRunRefusal is why a rollback run is asked for again rather than
// resumed or re-run; the call and the run's actions say exactly this.
func (st *RunState) rollbackRunRefusal() string {
	r := st.restoreStep().Restore
	return fmt.Sprintf("this is a rollback run: a half-done rollback is never continued, and a stale one is not repeated. Ask for the rollback again "+
		"from the environment or application page (launchway deployments rollback --env %s --to %s --reason ...), which checks what runs now",
		st.restoreStep().Environment, r.To.Deployment)
}

// startRestore moves the rollback step of a rollback run towards its first
// action: it takes the environment's lock and opens a deployment, or waits
// for the lock. A rollback is never judged by policies (it returns to what
// ran before) and never skipped for a newer waiting run.
func (c *cmd) startRestore(id string, def *pipeline.Step) {
	st := c.st
	s := st.Steps[id]
	r := def.Restore
	if why := st.deployRefused(); why != "" {
		c.failStep(id, why)
		return
	}
	env, ok := c.e.live().envs[def.Environment]
	if !ok {
		c.failStep(id, fmt.Sprintf("environment %s is not configured on this server", def.Environment))
		return
	}
	u := unit{def.Environment, st.Project, def.Service}
	holder, err := c.lockHolder(u)
	if err != nil {
		c.dbFail(err)
		return
	}
	if holder != nil {
		if s.Status == StepWaitingS && s.WaitKind == WaitLock && s.Dep != nil && s.Dep.Holder != nil && s.Dep.Holder.Deployment == holder.Deployment {
			return // already waiting for this holder
		}
		c.emit(DeployWaitingLock, Payload{Step: id, Deploy: &DeployEvent{Environment: u.env, Service: u.service, Holder: holder}})
		return
	}
	// What was true when the rollback was asked for must still be: it was
	// worked out from what the service ran then.
	hist, err := unitHistory(c.tx, u.project, u.env, u.service)
	if err != nil {
		c.dbFail(err)
		return
	}
	stack, _ := inEffect(hist)
	switch {
	case len(stack) == 0 || stack[len(stack)-1].ID != r.From.Deployment:
		now := "nothing it knows of"
		if len(stack) > 0 {
			now = stack[len(stack)-1].Version + " (run #" + fmt.Sprint(stack[len(stack)-1].RunNumber) + ")"
		}
		c.failStep(id, fmt.Sprintf("%s ran %s (run #%d) when this rollback was asked for, but runs %s now; nothing was changed. Ask again to roll back what runs now",
			def.Service, r.From.Version, r.From.RunNumber, now))
		return
	}
	depID := ids.New("dep")
	if _, err := c.tx.Exec(`INSERT INTO deploy_locks(environment, project, service, deployment, run_id, run_number, step_id, since)
		VALUES(?,?,?,?,?,?,?,?)`, u.env, u.project, u.service, depID, st.ID, st.Number, id, c.now); err != nil {
		c.dbFail(err)
		return
	}
	prev := &DeployRef{Deployment: r.From.Deployment, Run: r.From.Run, Number: r.From.RunNumber, Version: r.From.Version}
	deadline := c.now + def.Timeout.D().Milliseconds()
	c.emit(DeployOpened, Payload{Step: id, Deploy: &DeployEvent{ID: depID, Environment: u.env, Service: u.service, Target: env.Target,
		Application: def.ArgoCD.Application, Mode: "rollback", Version: r.To.Version, Previous: prev, Deadline: deadline, Protected: &env.Protected}})
	c.emit(StepQueued, Payload{Step: id, Attempt: s.Attempt + 1, Deploy: &DeployEvent{ID: depID, Action: ActionRevert}})
	c.after(c.e.kickDeploys)
}

// restoreJob fills a deploy runner's job for the revert of a rollback run.
func (c *cmd) restoreJob(def *pipeline.Step, s *StepState) *DeployJob {
	st := c.st
	r, dep := def.Restore, s.Dep
	dj := &DeployJob{Action: ActionRevert, Deployment: dep.ID, Environment: dep.Environment, Service: dep.Service, Target: dep.Target,
		Application: dep.Application, Version: dep.Version, PreviousVersion: r.From.Version,
		Repo: def.ArgoCD.WriteBack.Repo, Branch: def.ArgoCD.WriteBack.Branch, Sync: def.ArgoCD.Sync, Deadline: s.Deadline}
	dj.Message = fmt.Sprintf("Roll back %s in %s from %s to %s\n\nRun #%d of %s, asked for by %s: %s.",
		dep.Service, dep.Environment, r.From.Version, r.To.Version, st.Number, st.Project, r.Actor, r.Reason)
	for _, rv := range r.Reverts {
		dj.Reverts = append(dj.Reverts, DeployRevert{Deployment: rv.Deployment, Version: rv.Version, Commit: rv.Commit, Run: rv.Run, RunNumber: rv.RunNumber,
			Message: fmt.Sprintf("Roll back %s in %s: revert %s (run #%d)\n\nRestoring %s; run #%d of %s, asked for by %s: %s.",
				dep.Service, dep.Environment, rv.Version, rv.RunNumber, r.To.Version, st.Number, st.Project, r.Actor, r.Reason)})
	}
	return dj
}

// DeployRevert is one deployment a revert action undoes (a rollback run).
type DeployRevert struct {
	Deployment string `json:"deployment"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Run        string `json:"run"`
	RunNumber  int    `json:"run_number"`
	Message    string `json:"message"`
}

// checkEmergency checks a setting for emergency rollbacks.
func checkEmergency(v, where string) error {
	switch v {
	case "", EmergencyAllow, EmergencyRequireApproval:
		return nil
	}
	return fmt.Errorf("%s: %q: write %s or %s", where, v, EmergencyAllow, EmergencyRequireApproval)
}

// advanceRestore moves the rollback step of a rollback run: asked for by a
// person, it runs when the steps it needs (an approval, if one is required)
// have succeeded. It reports whether it changed the step.
func (c *cmd) advanceRestore(id string, def *pipeline.Step) bool {
	ready, blocked := c.needsState(def)
	switch {
	case blocked != "":
		c.emit(StepSkipped, Payload{Step: id, SkipKind: "upstream", Reason: "not run because " + blocked})
		return true
	case ready:
		before := len(c.events)
		c.startRestore(id, def)
		return len(c.events) > before
	}
	return false
}

// checkRollbackRun refuses a rollback run on a server that does not know
// them: they are read, and acted on, only by servers that know the restore
// key of the step and the review event.
func (e *Engine) checkRollbackRun(nr *NewRun) error {
	if nr.Trigger.Event == pipeline.EventRollback && !e.knows(schemaRollback) {
		return fmt.Errorf("%w: this server does not know rollback runs (schema %d)", ErrConflict, schemaRollback)
	}
	return nil
}
