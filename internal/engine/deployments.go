package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/deploy/edit"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Deployments.
//
// A deploy step deploys a service of a project to an environment. Each
// (environment, project, service) has one lock: the deploy step takes it in
// the transaction that opens its deployment and queues its first action, and
// it is released when every step of the deployment (the deploy step, its
// rollback step, and every step on a needs path between them) is done. A
// deploy step that finds the lock held waits (deploy.waiting_lock); with
// queue: newest, an older run waiting for the same lock is skipped as
// superseded.
//
// A step's work is done by deploy runners serving the environment's target,
// in short actions leased like any step but by target and without a
// workspace: apply (commit the version to Git, ask Argo CD to sync) for a
// deploy step, revert (revert that commit, ask Argo CD to sync) for a
// rollback step. Between actions the step waits, holding no lease, until
// deploy runners observe that it is done (see observe.go), or its deadline
// passes.
//
// The rows of deployments, environment_state, deploy_locks and
// deploy_queue are derived from the events, in the transaction that stores
// them.

// Skip kinds of schema 11.
const (
	// SkipNotNeeded: a rollback step whose deployment was never made, or
	// whose guarding steps all succeeded.
	SkipNotNeeded = "not_needed"
	// SkipSuperseded: a deploy step that waited for its environment's lock
	// while a newer run of the same service started waiting too (queue:
	// newest).
	SkipSuperseded = "superseded"
)

// Verdicts: how a deployment ended (deploy.closed).
const (
	VerdictDeployed       = "deployed"        // the deploy step succeeded and nothing rolled it back
	VerdictFailed         = "failed"          // the deploy step failed and nothing rolled it back
	VerdictRolledBack     = "rolled_back"     // the rollback step reverted it and the environment is healthy again
	VerdictRollbackFailed = "rollback_failed" // the rollback step failed: the environment needs a person
	VerdictAbandoned      = "abandoned"       // canceled before it was deployed
)

// Actions of deploy runners.
const (
	ActionApply  = "apply"
	ActionRevert = "revert"
)

// Environment is a place deploy steps deploy to. Environments are in the
// server's configuration; every server sharing a database must have the
// same ones.
type Environment struct {
	Name   string
	Target string // what deploy runners serve: the cluster the environment is in
	// Projects are the projects (patterns, as in mcp-*) that may deploy here.
	Projects []string
	// Queue is newest (the default: a run waiting for the lock is skipped
	// when a newer one starts waiting) or all (they deploy in turn).
	Queue string
	// Protected: the person who started a run may not promote (or retry)
	// its deployment here unless approvals.allow_self says they may
	// approve their own runs; anyone with the operator role here may abort
	// it (schema 14).
	Protected bool
}

// Allows reports whether a project may deploy to the environment.
func (env Environment) Allows(project string) bool {
	for _, p := range env.Projects {
		if ok, _ := path.Match(p, project); ok {
			return true
		}
	}
	return false
}

// checkEnvironments checks a list of environments from the configuration.
func checkEnvironments(envs []Environment) (map[string]Environment, error) {
	out := map[string]Environment{}
	for _, env := range envs {
		switch {
		case !validName(env.Name):
			return nil, fmt.Errorf("environment %q: use lower-case letters, digits and hyphens", env.Name)
		case out[env.Name].Name != "":
			return nil, fmt.Errorf("environment %s is listed twice", env.Name)
		case !validName(env.Target):
			return nil, fmt.Errorf("environment %s: target %q: use lower-case letters, digits and hyphens", env.Name, env.Target)
		case len(env.Projects) == 0:
			return nil, fmt.Errorf("environment %s: list the projects that may deploy to it under projects (patterns such as mcp-* work)", env.Name)
		}
		for _, p := range env.Projects {
			if _, err := path.Match(p, ""); err != nil {
				return nil, fmt.Errorf("environment %s: %q is not a valid pattern", env.Name, p)
			}
		}
		switch env.Queue {
		case "":
			env.Queue = "newest"
		case "newest", "all":
		default:
			return nil, fmt.Errorf("environment %s: queue %q: write newest or all", env.Name, env.Queue)
		}
		out[env.Name] = env
	}
	return out, nil
}

func validName(s string) bool {
	if s == "" || len(s) > 63 || s[0] == '-' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Environments returns the configured environments, by name.
func (e *Engine) Environments() []Environment {
	out := make([]Environment, 0, len(e.live().envs))
	for _, env := range e.live().envs {
		out = append(out, env)
	}
	sortEnvs(out)
	return out
}

// CheckDeploys is the check of a pipeline against the server's
// configuration: every environment its deploy steps name exists and allows
// the project (unless project is empty). Problems carry the lines of the
// file, when known.
func (e *Engine) CheckDeploys(project string, p *pipeline.Pipeline) pipeline.Problems {
	var out pipeline.Problems
	for _, s := range p.Steps {
		if !s.IsDeploy() {
			continue
		}
		line := s.EnvironmentLine()
		env, ok := e.live().envs[s.Environment]
		switch {
		case !ok && len(e.live().envs) == 0:
			out = append(out, pipeline.Problem{Line: line, Path: "steps." + s.ID + ".environment",
				Message: fmt.Sprintf("%s: this server has no environments configured, so it cannot deploy to %s", s.ID, s.Environment)})
		case !ok:
			out = append(out, pipeline.Problem{Line: line, Path: "steps." + s.ID + ".environment",
				Message: fmt.Sprintf("%s: environment %q is not configured on this server; it knows %s", s.ID, s.Environment, envNames(e.live().envs))})
		case project != "" && !env.Allows(project):
			out = append(out, pipeline.Problem{Line: line, Path: "steps." + s.ID + ".environment",
				Message: fmt.Sprintf("%s: project %s may not deploy to %s (see the environment's projects in the server's configuration)", s.ID, project, s.Environment)})
		}
	}
	return out
}

func envNames(envs map[string]Environment) string {
	list := make([]Environment, 0, len(envs))
	for _, e := range envs {
		list = append(list, e)
	}
	sortEnvs(list)
	names := make([]string, len(list))
	for i, e := range list {
		names[i] = e.Name
	}
	return strings.Join(names, ", ")
}

// unit is what a lock is for.
type unit struct{ env, project, service string }

func (st *RunState) unitOf(def *pipeline.Step) unit {
	service := def.Service
	if service == "" {
		service = st.Project
	}
	return unit{def.Environment, st.Project, service}
}

// holdsDeployment reports whether the run holds an open deployment, so that
// a newer run superseding it must not cancel it.
func (st *RunState) holdsDeployment() bool {
	for _, s := range st.Steps {
		if s.Dep.Opened() && !s.Dep.Closed && st.Pipeline.Step(s.ID).IsDeploy() {
			return true
		}
	}
	return false
}

// openedDeployment names a deploy step of the run that opened a deployment,
// or "".
func (st *RunState) openedDeployment() string {
	for _, id := range st.Order {
		if s := st.Steps[id]; s.Dep.Opened() && st.Pipeline.Step(id).IsDeploy() {
			return id
		}
	}
	return ""
}

// queueAction records the action a step.queued event queues.
func (st *RunState) queueAction(s *StepState, d *DeployEvent) {
	if s.Dep == nil || s.Dep.ID != d.ID {
		// A rollback step takes over its deploy step's deployment.
		dep := &DeployState{ID: d.ID}
		if def := st.Pipeline.Step(s.ID); def != nil && def.IsRollback() {
			if from := st.Steps[def.Deployment]; from != nil && from.Dep != nil {
				c := *from.Dep
				c.Facts, c.Phase, c.Detail, c.Phases, c.Closed, c.Verdict, c.Commit = nil, "", "", 0, false, "", ""
				dep = &c
			}
		}
		s.Dep = dep
	}
	s.Dep.Action, s.Dep.ActionDone = d.Action, false
	if d.Deadline > 0 {
		s.Deadline = d.Deadline
	}
}

// applyDeploy folds the deploy.* events.
func (st *RunState) applyDeploy(ev Event, s *StepState) {
	p := ev.Data
	d := p.Deploy
	if d == nil || s == nil {
		return
	}
	switch ev.Type {
	case DeployWaitingLock:
		s.Status = StepWaitingS
		s.WaitKind = WaitLock
		if s.Dep == nil || s.Dep.Opened() {
			s.QueuedAt, s.StartedAt, s.FinishedAt = ev.At, ev.At, 0
		}
		s.Error, s.Deadline = "", 0
		s.Dep = &DeployState{Environment: d.Environment, Service: d.Service, Holder: d.Holder}
		if st.Status == RunQueued {
			st.Status, st.StartedAt = RunRunning, ev.At
		}
	case DeployOpened:
		s.Dep = &DeployState{ID: d.ID, Environment: d.Environment, Service: d.Service, Target: d.Target,
			Application: d.Application, Mode: d.Mode, Version: d.Version, Previous: d.Previous, OpenedAt: ev.At}
		s.Deadline = d.Deadline
		s.WaitKind = ""
		if st.Status == RunQueued {
			st.Status, st.StartedAt = RunRunning, ev.At
		}
	case DeployActionDone:
		if s.Dep != nil {
			s.Dep.ActionDone = true
			s.Dep.Facts = d.Facts
		}
	case DeployObserving:
		if def := st.Pipeline.Step(ev.Data.Step); def != nil && def.IsRolloutStep() && d.ID != "" {
			st.adoptDeployment(s, d.ID, ev.At)
		}
		s.Status = StepWaitingS
		s.WaitKind = WaitObserve
		s.LeaseID = ""
		if d.Deadline > 0 {
			s.Deadline = d.Deadline
		}
		if s.Dep != nil {
			s.Dep.Commit = d.Commit
		}
	case DeployPhase:
		if s.Dep != nil {
			s.Dep.Phase, s.Dep.Detail = d.Phase, d.Detail
			s.Dep.Phases++
		}
	case DeployClosed:
		if s.Dep != nil {
			s.Dep.Closed, s.Dep.Verdict = true, d.Verdict
		}
	default:
		st.applyControl(ev, s)
	}
}

// ---- moving deploy and rollback steps forward (called from advance) ----

// failStep fails a step that never started, saying why.
func (c *cmd) failStep(id, reason string) {
	c.emit(StepFinished, Payload{Step: id, Outcome: string(StepFailed), Reason: reason})
}

// dbFail records a database error met while moving the run forward; the
// command then fails with it (and is tried again if it is a conflict).
func (c *cmd) dbFail(err error) {
	if c.err == nil {
		c.err = err
	}
}

// deployRefused says why a deploy step may not run in this run, or "".
func (st *RunState) deployRefused() string {
	switch {
	case st.Draft != nil:
		return "deploy steps do not run in draft runs: their text has not been saved and reviewed"
	case st.Trigger.Event == pipeline.EventPullRequest:
		return "deploy steps do not run in pull-request runs"
	case st.Source.Kind != "git":
		return "a deploy step deploys a commit from Git, so it runs only on a server, not with `launchway run`"
	}
	return ""
}

// startDeploy moves a deploy step whose needs are met towards its first
// action: it takes the environment's lock and opens the deployment, or
// waits for the lock.
func (c *cmd) startDeploy(id string, def *pipeline.Step) {
	st := c.st
	s := st.Steps[id]
	if why := st.deployRefused(); why != "" {
		c.failStep(id, why)
		return
	}
	env, ok := c.e.live().envs[def.Environment]
	if !ok {
		c.failStep(id, fmt.Sprintf("environment %s is not configured on this server", def.Environment))
		return
	}
	if !env.Allows(st.Project) {
		c.failStep(id, fmt.Sprintf("project %s may not deploy to %s", st.Project, env.Name))
		return
	}
	version, err := c.resolve(def, def.Version)
	if err == nil {
		_, err = c.resolveChanges(def)
	}
	if err != nil {
		c.failStep(id, err.Error())
		return
	}
	u := st.unitOf(def)
	holder, err := c.lockHolder(u)
	if err != nil {
		c.dbFail(err)
		return
	}
	if holder != nil {
		c.waitForLock(id, def, env, u, holder)
		return
	}
	// Before anything reaches the environment, the policies judge the run.
	if !c.gateDeploy(id, def, def, env.Name, "deploy") {
		return
	}
	// The lock is free: take it. Another server taking it at the same moment
	// makes one of the two inserts fail, and that command is tried again.
	depID := ids.New("dep")
	if _, err := c.tx.Exec(`INSERT INTO deploy_locks(environment, project, service, deployment, run_id, run_number, step_id, since)
		VALUES(?,?,?,?,?,?,?,?)`, u.env, u.project, u.service, depID, st.ID, st.Number, id, c.now); err != nil {
		c.dbFail(err)
		return
	}
	prev, err := c.previous(u)
	if err != nil {
		c.dbFail(err)
		return
	}
	deadline := c.now + def.Timeout.D().Milliseconds()
	c.emit(DeployOpened, Payload{Step: id, Deploy: &DeployEvent{ID: depID, Environment: u.env, Service: u.service, Target: env.Target,
		Application: def.ArgoCD.Application, Mode: "write_back", Version: version, Previous: prev, Deadline: deadline}})
	c.emit(StepQueued, Payload{Step: id, Attempt: s.Attempt + 1, Deploy: &DeployEvent{ID: depID, Action: ActionApply}})
	c.after(c.e.kickDeploys)
}

// waitForLock makes a deploy step wait for the lock, superseding older
// waiters (or itself) under queue: newest.
func (c *cmd) waitForLock(id string, def *pipeline.Step, env Environment, u unit, holder *DeployRef) {
	st := c.st
	s := st.Steps[id]
	if env.Queue != "all" {
		rows, err := c.tx.Query(`SELECT run_id, step_id, run_number FROM deploy_queue
			WHERE environment = ? AND project = ? AND service = ? AND run_id <> ?`, u.env, u.project, u.service, st.ID)
		if err != nil {
			c.dbFail(err)
			return
		}
		type waiter struct {
			run, step string
			number    int
		}
		var ws []waiter
		for rows.Next() {
			var w waiter
			if err := rows.Scan(&w.run, &w.step, &w.number); err != nil {
				rows.Close()
				c.dbFail(err)
				return
			}
			ws = append(ws, w)
		}
		rows.Close()
		for _, w := range ws {
			if w.number > st.Number {
				c.emit(StepSkipped, Payload{Step: id, SkipKind: SkipSuperseded,
					Reason: fmt.Sprintf("superseded at %s by run #%d", u.env, w.number)})
				return
			}
		}
		for _, w := range ws {
			err := c.alsoRun(w.run, func(o *cmd) error {
				os := o.st.Steps[w.step]
				if os == nil || os.Status != StepWaitingS || os.WaitKind != WaitLock {
					return nil
				}
				o.emit(StepSkipped, Payload{Step: w.step, SkipKind: SkipSuperseded,
					Reason: fmt.Sprintf("superseded at %s by run #%d", u.env, st.Number)})
				o.advance()
				return o.err
			})
			if err != nil {
				c.dbFail(err)
				return
			}
		}
	}
	if s.Status == StepWaitingS && s.WaitKind == WaitLock && s.Dep != nil && s.Dep.Holder != nil && s.Dep.Holder.Deployment == holder.Deployment {
		return // already waiting for this holder
	}
	c.emit(DeployWaitingLock, Payload{Step: id, Deploy: &DeployEvent{Environment: u.env, Service: u.service, Holder: holder}})
}

// lockHolder returns the deployment holding a unit's lock, or nil.
func (c *cmd) lockHolder(u unit) (*DeployRef, error) {
	var r DeployRef
	err := c.tx.QueryRow(`SELECT l.deployment, l.run_id, l.run_number, l.step_id, COALESCE(d.version, '') FROM deploy_locks l
		LEFT JOIN deployments d ON d.id = l.deployment WHERE l.environment = ? AND l.project = ? AND l.service = ?`,
		u.env, u.project, u.service).Scan(&r.Deployment, &r.Run, &r.Number, &r.Step, &r.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// previous returns what Launchway last deployed to a unit, or nil.
func (c *cmd) previous(u unit) (*DeployRef, error) {
	var r DeployRef
	err := c.tx.QueryRow(`SELECT current_deployment, current_run, current_run_number, current_version FROM environment_state
		WHERE environment = ? AND project = ? AND service = ?`, u.env, u.project, u.service).Scan(&r.Deployment, &r.Run, &r.Number, &r.Version)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.Deployment == "" && r.Version == "") {
		return nil, nil
	}
	return &r, err
}

// advanceRollback decides a rollback step once every step it needs is done:
// it runs if and only if its deployment was opened and one of those steps
// did not succeed (failed, or was skipped or canceled because something
// failed). It reports whether it changed the step.
func (c *cmd) advanceRollback(id string, def *pipeline.Step) bool {
	st := c.st
	bad := ""
	for _, n := range def.Needs {
		dep := st.Steps[n]
		switch dep.Status {
		case StepSucceeded:
		case StepSkippedS:
			if dep.SkipKind != "condition" && dep.SkipKind != SkipNotNeeded && bad == "" {
				bad = n + " was skipped (" + dep.Error + ")"
			}
		case StepFailed, StepCanceledS:
			if bad == "" {
				bad = fmt.Sprintf("%s %s", n, dep.Status)
			}
		default:
			return false // not every step it needs is done
		}
	}
	d := st.Steps[def.Deployment]
	switch {
	case d == nil || !d.Dep.Opened():
		c.emit(StepSkipped, Payload{Step: id, SkipKind: SkipNotNeeded, Reason: "its deployment was never made"})
	case bad == "":
		c.emit(StepSkipped, Payload{Step: id, SkipKind: SkipNotNeeded, Reason: "every step it needs succeeded"})
	case d.Dep.Closed:
		// Cannot happen: the deployment stays open until its rollback step is
		// done. Refuse rather than revert without the lock.
		c.failStep(id, "its deployment is already closed, so it holds no lock to roll back with")
	default:
		s := st.Steps[id]
		c.emit(StepQueued, Payload{Step: id, Attempt: s.Attempt + 1, Reason: "rolling back because " + bad,
			Deploy: &DeployEvent{ID: d.Dep.ID, Action: ActionRevert, Deadline: c.now + def.Timeout.D().Milliseconds()}})
		c.after(c.e.kickDeploys)
	}
	return true
}

// closeDeployments closes the deployments whose steps are all done,
// releasing their locks.
func (c *cmd) closeDeployments() {
	st := c.st
	for _, id := range st.Order {
		s := st.Steps[id]
		def := st.Pipeline.Step(id)
		if !def.IsDeploy() || !s.Dep.Opened() || s.Dep.Closed {
			continue
		}
		scope := st.Pipeline.DeploymentScope(id)
		done := true
		for _, m := range scope {
			if !st.Steps[m].Status.Terminal() {
				done = false
				break
			}
		}
		if !done {
			continue
		}
		verdict := VerdictFailed
		rb := st.Pipeline.RollbackOf(id)
		var rbs *StepState
		if rb != nil {
			rbs = st.Steps[rb.ID]
		}
		switch {
		case rbs != nil && rbs.Status == StepSucceeded:
			verdict = VerdictRolledBack
		case rbs != nil && rbs.Status == StepFailed:
			verdict = VerdictRollbackFailed
		case s.Status == StepSucceeded:
			verdict = VerdictDeployed
			if v := st.controlVerdict(id); v != "" {
				verdict = v
			}
		case s.Status == StepCanceledS:
			verdict = VerdictAbandoned
		}
		c.emit(DeployClosed, Payload{Step: id, Deploy: &DeployEvent{ID: s.Dep.ID, Verdict: verdict}})
		c.after(c.e.kickDeploys)
	}
}

// resolve works out a value for a step of the run.
func (c *cmd) resolve(def *pipeline.Step, v *pipeline.Value) (string, error) {
	st := c.st
	switch {
	case v == nil:
		return "", nil
	case v.Output != nil:
		step, key := v.Output.Step, v.Output.Key
		if g := st.Pipeline.Group(step); g != nil {
			ref, ok := g.Outputs[key]
			if !ok {
				return "", fmt.Errorf("%s does not expose an output %s", step, key)
			}
			step, key = ref.Step, ref.Key
		}
		dep := st.Steps[step]
		if dep == nil {
			return "", fmt.Errorf("there is no step %s", step)
		}
		val, ok := dep.Outputs[key]
		if !ok || strings.TrimSpace(val) == "" {
			return "", fmt.Errorf("%s did not write the output %s (write %s=... to the file named by $LAUNCHWAY_OUTPUTS)", v.Output.Step, v.Output.Key, v.Output.Key)
		}
		return strings.TrimSpace(val), nil
	case v.Run != "":
		t := st.Trigger
		var val string
		switch v.Run {
		case "sha":
			val = t.SHA
		case "short_sha":
			val = t.SHA[:min(len(t.SHA), 8)]
		case "branch":
			val = t.Branch
		case "tag":
			val = t.Tag
		case "number":
			val = strconv.Itoa(st.Number)
		}
		if val == "" {
			return "", fmt.Errorf("the run has no %s (it was started from %s)", v.Run, nonEmpty(t.Ref, "no ref"))
		}
		return val, nil
	}
	return v.Literal, nil
}

// resolveChanges works out a deploy step's changes.
func (c *cmd) resolveChanges(def *pipeline.Step) ([]DeployChange, error) {
	if def.ArgoCD == nil || def.ArgoCD.WriteBack == nil {
		return nil, errors.New("the step says nothing to commit")
	}
	var out []DeployChange
	for _, ch := range def.ArgoCD.WriteBack.Changes {
		dc := DeployChange{File: ch.File, Image: ch.Image, Set: ch.Set}
		var err error
		switch {
		case ch.Image != "" && ch.Digest != nil:
			if dc.Digest, err = c.resolve(def, ch.Digest); err == nil {
				err = edit.CheckDigest(dc.Digest)
			}
		case ch.Image != "":
			dc.Tag, err = c.resolve(def, ch.Tag)
		default:
			dc.Value, err = c.resolve(def, ch.Value)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ch.File, err)
		}
		if strings.ContainsAny(dc.Tag+dc.Digest+dc.Value, "\r\n") {
			return nil, fmt.Errorf("%s: the value is more than one line", ch.File)
		}
		out = append(out, dc)
	}
	return out, nil
}

// ---- what the deploy step's runs tables say ----

// persistSchema11 brings the deployment tables up to date with the
// command's events.
func (c *cmd) persistSchema11() error {
	st := c.st
	for _, id := range c.changedSteps() {
		def := st.Pipeline.Step(id)
		if def == nil || !def.IsDeployKind() {
			continue
		}
		s := st.Steps[id]
		target := ""
		if s.Dep != nil {
			target = s.Dep.Target
		}
		if _, err := c.tx.Exec(`UPDATE steps SET target = ?, wait_kind = ? WHERE run_id = ? AND step_id = ?`,
			target, s.WaitKind, st.ID, id); err != nil {
			return err
		}
		if s.WaitKind != WaitLock {
			if _, err := c.tx.Exec(`DELETE FROM deploy_queue WHERE run_id = ? AND step_id = ?`, st.ID, id); err != nil {
				return err
			}
		}
	}
	for _, ev := range c.events {
		d := ev.Data.Deploy
		s := st.Steps[ev.Data.Step]
		var err error
		switch ev.Type {
		case DeployWaitingLock:
			_, err = c.tx.Exec(`INSERT INTO deploy_queue(run_id, step_id, environment, project, service, run_number, since)
				VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id, step_id) DO NOTHING`,
				st.ID, ev.Data.Step, d.Environment, st.Project, d.Service, st.Number, ev.At)
		case DeployOpened:
			prevID, prevVersion := "", ""
			if d.Previous != nil {
				prevID, prevVersion = d.Previous.Deployment, d.Previous.Version
			}
			_, err = c.tx.Exec(`INSERT INTO deployments(id, run_id, run_number, project, step_id, environment, service, target,
					application, mode, version, previous_id, previous_version, opened_at)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, d.ID, st.ID, st.Number, st.Project, ev.Data.Step, d.Environment, d.Service,
				d.Target, d.Application, d.Mode, d.Version, prevID, prevVersion, ev.At)
		case StepQueued:
			if d != nil && d.Action == ActionRevert {
				_, err = c.tx.Exec(`UPDATE deployments SET rollback_step = ? WHERE id = ?`, ev.Data.Step, d.ID)
			}
		case DeployActionDone:
			if f := d.Facts; f != nil && s != nil && s.Dep != nil {
				if s.Dep.Action == ActionRevert {
					_, err = c.tx.Exec(`UPDATE deployments SET revert_sha = ? WHERE id = ?`, f.Revert, s.Dep.ID)
				} else {
					_, err = c.tx.Exec(`UPDATE deployments SET repo = ?, branch = ?, commit_sha = ?, changed = ? WHERE id = ?`,
						f.Repo, f.Branch, f.Commit, boolInt(f.Changed), s.Dep.ID)
				}
			}
		case DeployPhase:
			if s != nil && s.Dep != nil {
				_, err = c.tx.Exec(`UPDATE deployments SET phase = ?, detail = ? WHERE id = ?`, d.Phase, d.Detail, s.Dep.ID)
			}
		case StepFinished:
			err = c.deployFinished(ev, s)
		case DeployClosed:
			if _, err = c.tx.Exec(`UPDATE deployments SET verdict = ?, closed_at = ? WHERE id = ?`, d.Verdict, ev.At, d.ID); err == nil {
				_, err = c.tx.Exec(`DELETE FROM deploy_locks WHERE deployment = ?`, d.ID)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// deployFinished records in environment_state what a finished deploy or
// rollback step left running.
func (c *cmd) deployFinished(ev Event, s *StepState) error {
	st := c.st
	def := st.Pipeline.Step(ev.Data.Step)
	if s == nil || def == nil || !def.IsDeployKind() || !s.Dep.Opened() || ev.Data.Outcome != string(StepSucceeded) {
		return nil
	}
	dep := s.Dep
	if def.IsDeploy() && def.StopsAtPause() {
		return nil // at a pause: deployed once a rollout step gets it healthy
	}
	if def.IsRolloutStep() && (def.Action == "abort" || def.Until != "healthy") {
		return nil
	}
	if def.IsDeploy() || def.IsRolloutStep() {
		if _, err := c.tx.Exec(`UPDATE deployments SET deployed_at = ? WHERE id = ?`, ev.At, dep.ID); err != nil {
			return err
		}
		_, err := c.tx.Exec(`INSERT INTO environment_state(environment, project, service, current_deployment, current_version,
				current_run, current_run_number, since) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(environment, project, service) DO UPDATE SET current_deployment = excluded.current_deployment,
				current_version = excluded.current_version, current_run = excluded.current_run,
				current_run_number = excluded.current_run_number, since = excluded.since`,
			dep.Environment, st.Project, dep.Service, dep.ID, dep.Version, st.ID, st.Number, ev.At)
		return err
	}
	if dep.Facts == nil || !dep.Facts.Reverted {
		return nil // nothing was reverted: the environment runs what it ran
	}
	prev := dep.Previous
	if prev == nil {
		prev = &DeployRef{}
	}
	_, err := c.tx.Exec(`INSERT INTO environment_state(environment, project, service, current_deployment, current_version,
			current_run, current_run_number, since) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(environment, project, service) DO UPDATE SET current_deployment = excluded.current_deployment,
			current_version = excluded.current_version, current_run = excluded.current_run,
			current_run_number = excluded.current_run_number, since = excluded.since`,
		dep.Environment, st.Project, dep.Service, prev.Deployment, prev.Version, prev.Run, prev.Number, ev.At)
	return err
}

// ---- deadlines ----

// expireDeploy fails a deploy or rollback step whose deadline has passed
// while it was queued or waiting for runners to observe it, saying what was
// last observed.
func (c *cmd) expireDeploy(id string, def *pipeline.Step) error {
	s := c.st.Steps[id]
	reason := ""
	switch {
	case s.Status == StepQueuedS:
		target := ""
		if s.Dep != nil {
			target = s.Dep.Target
		}
		reason = fmt.Sprintf("no deploy runner serving target %s took the step within %s", target, def.Timeout.Short())
	case s.Status == StepWaitingS && s.WaitKind == WaitObserve:
		var phase, detail, by string
		var at int64
		if s.Dep != nil {
			err := c.tx.QueryRow(`SELECT phase, detail, observed_by, observed_at FROM deployments WHERE id = ?`, s.Dep.ID).Scan(&phase, &detail, &by, &at)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		what := "healthy"
		switch {
		case def.IsRollback():
			what = "healthy on the reverted version"
		case def.IsRolloutStep() && def.Action == "abort":
			what = "aborted"
		case def.IsRolloutStep() && def.Until == "paused":
			what = "at the rollout's next pause"
		case def.IsDeploy() && def.StopsAtPause():
			what = "at the rollout's first pause"
		}
		reason = fmt.Sprintf("not %s within %s", what, def.Timeout.Short())
		if at == 0 {
			reason += "; no deploy runner observed it"
		} else {
			ago := (c.now - at) / 1000
			reason += fmt.Sprintf("; last observed %ds ago by %s: %s", ago, by, detail)
		}
	default:
		return nil
	}
	c.emit(StepFinished, Payload{Step: id, Attempt: s.Attempt, Outcome: string(StepFailed), Reason: reason})
	c.advance()
	return nil
}

// DeployChange is one resolved change of a deploy step.
type DeployChange struct {
	File  string `json:"file"`
	Image string `json:"image,omitempty"`
	Tag   string `json:"tag,omitempty"`
	// Digest pins the image (schema 15). A change has a tag or a digest,
	// never both, so a deploy runner older than this, which knows only Tag,
	// refuses the empty tag rather than writing a tag in its place.
	Digest string `json:"digest,omitempty"`
	Set    string `json:"set,omitempty"`
	Value  string `json:"value,omitempty"`
}

// DeployJob is what a deploy runner needs for one action.
type DeployJob struct {
	Action      string `json:"action"` // apply or revert
	Deployment  string `json:"deployment"`
	Environment string `json:"environment"`
	Service     string `json:"service"`
	Target      string `json:"target"`
	Application string `json:"application"`
	Version     string `json:"version"`
	// PreviousVersion is what the environment ran before, if known.
	PreviousVersion string `json:"previous_version,omitempty"`
	Repo            string `json:"repo"`
	Branch          string `json:"branch"`
	// Changes, for apply.
	Changes []DeployChange `json:"changes,omitempty"`
	// Commit, for revert: the deployment's commit, if the server knows it.
	// The runner finds it by its trailer either way.
	Commit string        `json:"commit,omitempty"`
	Sync   pipeline.Sync `json:"sync"`
	// Deadline is when the step fails if it is not done (Unix ms): an action
	// held by a sync window may wait until then.
	Deadline int64 `json:"deadline,omitempty"`
	// Message is the commit message (subject and body; the runner adds the
	// trailers).
	Message string `json:"message"`
	// AbortFirst, for a revert (schema 14): abort the rollout first, if it
	// is still on PodHash, the deployment's pod template hash ("" when
	// never seen: then nothing is aborted).
	AbortFirst bool                 `json:"abort_first,omitempty"`
	Rollout    *pipeline.RolloutRef `json:"rollout,omitempty"`
	PodHash    string               `json:"pod_hash,omitempty"`
}

// deployJob builds a deploy runner's job for a leased deploy or rollback step.
func (c *cmd) deployJob(stepID, leaseID string) (*Job, error) {
	st := c.st
	def := st.Pipeline.Step(stepID)
	s := st.Steps[stepID]
	deployDef := def
	if def.IsRollback() {
		deployDef = st.Pipeline.Step(def.Deployment)
	}
	dep := s.Dep
	dj := &DeployJob{Action: dep.Action, Deployment: dep.ID, Environment: dep.Environment, Service: dep.Service, Target: dep.Target,
		Application: dep.Application, Version: dep.Version, Repo: deployDef.ArgoCD.WriteBack.Repo, Branch: deployDef.ArgoCD.WriteBack.Branch,
		Sync: deployDef.ArgoCD.Sync, Deadline: s.Deadline}
	if dep.Previous != nil {
		dj.PreviousVersion = dep.Previous.Version
	}
	trig := st.Trigger
	if dep.Action == ActionApply {
		changes, err := c.resolveChanges(def)
		if err != nil {
			return nil, err
		}
		dj.Changes = changes
		dj.Message = fmt.Sprintf("Deploy %s %s to %s\n\nRun #%d of %s (pipeline %s), step %s, from commit %s.",
			dep.Service, dep.Version, dep.Environment, st.Number, st.Project, st.Pipeline.Name, stepID, trig.SHA[:min(len(trig.SHA), 12)])
	} else {
		if from := st.Steps[def.Deployment]; from != nil && from.Dep != nil && from.Dep.Facts != nil && from.Dep.Facts.Changed {
			dj.Commit = from.Dep.Facts.Commit
		}
		to := "the version before " + dep.Version
		if dj.PreviousVersion != "" {
			to = dj.PreviousVersion
		}
		dj.Message = fmt.Sprintf("Roll back %s in %s to %s\n\nRun #%d of %s, step %s: %s.",
			dep.Service, dep.Environment, to, st.Number, st.Project, stepID, nonEmpty(s.Error, "a step guarding the deployment did not succeed"))
		if def.AbortFirst && deployDef.Rollout != nil && c.e.knows(schemaControl) {
			row, err := c.deploymentRow(dep.ID)
			if err != nil {
				return nil, err
			}
			dj.AbortFirst, dj.Rollout, dj.PodHash = true, deployDef.Rollout, row.hash
		}
	}
	return &Job{LeaseID: leaseID, LeaseTTL: pipeline.Duration(c.e.ttl), RunID: st.ID, Project: st.Project, RunNumber: st.Number,
		Pipeline: st.Pipeline.Name, StepID: stepID, Attempt: s.Attempt, SHA: trig.SHA, Timeout: def.Timeout, Source: st.Source, Deploy: dj}, nil
}

// finishAction records how a deploy runner's action ended.
func (c *cmd) finishAction(stepID string, s *StepState, def *pipeline.Step, leaseID string, res Result) {
	st := c.st
	p := Payload{Step: stepID, Attempt: s.Attempt, Lease: leaseID}
	action := ""
	if s.Dep != nil {
		action = s.Dep.Action
	}
	switch res.Outcome {
	case "succeeded":
		f := res.Deploy
		if f == nil {
			p.Outcome, p.Reason = string(StepFailed), "the deploy runner reported no facts about what it did; it may be too old"
			c.emit(StepFinished, p)
			return
		}
		c.emit(DeployActionDone, Payload{Step: stepID, Attempt: s.Attempt, Deploy: &DeployEvent{Action: action, Outcome: "succeeded", Facts: f}})
		commit := f.Commit
		if action == ActionRevert {
			commit = f.Revert
		}
		switch {
		case st.CancelRequested:
			p.Reason = st.stoppedBecause()
			c.emit(StepCanceled, p)
		case action == ActionRevert && !f.Reverted:
			c.emit(DeployPhase, Payload{Step: stepID, Attempt: s.Attempt, Deploy: &DeployEvent{Phase: "nothing_to_revert",
				Detail: "nothing to revert: the deployment changed nothing in Git"}})
			p.Outcome = string(StepSucceeded)
			p.Outputs = map[string]string{"version": s.Dep.Version}
			c.emit(StepFinished, p)
		case commit == "":
			p.Outcome, p.Reason = string(StepFailed), "the deploy runner did not say which commit to watch for"
			c.emit(StepFinished, p)
		case c.now >= s.Deadline && s.Deadline > 0:
			p.Outcome, p.Reason = string(StepFailed), fmt.Sprintf("the step's %s ran out before Argo CD could be watched", def.Timeout.Short())
			c.emit(StepFinished, p)
		default:
			c.emit(DeployObserving, Payload{Step: stepID, Attempt: s.Attempt, Deploy: &DeployEvent{Until: "healthy", Deadline: s.Deadline, Commit: commit}})
			c.after(c.e.notify)
		}
	case "lost":
		if st.CancelRequested {
			p.Reason = st.stoppedBecause()
			c.emit(StepCanceled, p)
		} else {
			c.lose(stepID, nonEmpty(res.Error, "the deploy runner could not do the action"))
		}
	case "canceled":
		if st.CancelRequested {
			p.Reason = st.stoppedBecause()
			c.emit(StepCanceled, p)
		} else {
			c.lose(stepID, nonEmpty(res.Error, "the deploy runner stopped the action"))
		}
	default:
		facts := res.Deploy
		c.emit(DeployActionDone, Payload{Step: stepID, Attempt: s.Attempt, Deploy: &DeployEvent{Action: action, Outcome: "failed", Facts: facts}})
		p.Outcome = string(StepFailed)
		p.Reason = nonEmpty(res.Error, "the deploy runner's action failed")
		p.Excerpt = CleanExcerpt(res.Excerpt)
		c.emit(StepFinished, p)
	}
}

// DeploymentView is a deployment as the run page and the Environments page
// show it.
type DeploymentView struct {
	ID              string          `json:"id"`
	RunID           string          `json:"run_id"`
	RunNumber       int             `json:"run_number"`
	Project         string          `json:"project"`
	Step            string          `json:"step"`
	RollbackStep    string          `json:"rollback_step,omitempty"`
	Environment     string          `json:"environment"`
	Service         string          `json:"service"`
	Target          string          `json:"target"`
	Application     string          `json:"application"`
	Mode            string          `json:"mode"`
	Version         string          `json:"version"`
	PreviousID      string          `json:"previous_id,omitempty"`
	PreviousVersion string          `json:"previous_version,omitempty"`
	Repo            string          `json:"repo,omitempty"`
	Branch          string          `json:"branch,omitempty"`
	Commit          string          `json:"commit,omitempty"`
	Changed         bool            `json:"changed"`
	Revert          string          `json:"revert,omitempty"`
	Phase           string          `json:"phase"`
	Detail          string          `json:"detail"`
	Verdict         string          `json:"verdict,omitempty"`
	OpenedAt        int64           `json:"opened_at"`
	DeployedAt      int64           `json:"deployed_at,omitempty"`
	ClosedAt        int64           `json:"closed_at,omitempty"`
	Snapshot        json.RawMessage `json:"snapshot,omitempty"`
	ObservedAt      int64           `json:"observed_at,omitempty"`
	ObservedBy      string          `json:"observed_by,omitempty"`
	Holding         bool            `json:"holding"` // it holds its environment's lock
	Timeline        []TimelineEntry `json:"timeline,omitempty"`
	// Schema 14: the deployment's pod template hash as first seen in its
	// rollout, the commands given to the rollout, and the runner observing
	// its target (empty when none does).
	PodHash         string        `json:"pod_hash,omitempty"`
	Commands        []CommandView `json:"commands,omitempty"`
	Observer        string        `json:"observer,omitempty"`
	ObserverExpires int64         `json:"observer_expires,omitempty"`
}

// TimelineEntry is one change in what was observed of a deployment.
type TimelineEntry struct {
	At     int64  `json:"at"`
	Step   string `json:"step"`
	Phase  string `json:"phase"`
	Detail string `json:"detail"`
}
