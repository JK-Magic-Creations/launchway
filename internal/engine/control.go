package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Controlling deployments (schema 14).
//
// A command is promote, promote-full, abort or retry, given to a
// deployment's Argo Rollouts rollout by a person (from the run page), by a
// rollout step, by a deploy step's pauses: promote, or by on_cancel: abort.
// It is an event (deploy.command) of the run that made the deployment,
// carrying what it expects of the rollout: the deployment's pod template
// hash and the canary step it was at. Because it is decided in the run's
// transaction, two people clicking Promote on two servers at once give one
// command: the second finds the first and is told who asked.
//
// Deploy runners serving the deployment's target take commands
// (deploy.command_taken, for a while) and apply them only if the rollout is
// still at what the command expects (deploy.command_done says applied,
// already applied, not applied, or failed). A command nobody has taken can
// be withdrawn. One taken by a runner that then stops is taken again once
// the take runs out; the check against the rollout makes that safe.
//
// The steps watching the deployment see the effect in what deploy runners
// observe, as for anything else that happens to the rollout. An approval
// that names the deployment is rejected by itself once the rollout is
// aborted.

// Where a command came from.
const (
	OriginPerson   = "person"    // someone, from the run page or the API
	OriginStep     = "step"      // a rollout step
	OriginPauses   = "pauses"    // a deploy or rollout step's pauses: promote
	OriginOnCancel = "on_cancel" // on_cancel: abort, when the run was canceled
)

// States of a command.
const (
	CommandPending   = "pending"
	CommandTaken     = "taken"
	CommandDone      = "done"
	CommandWithdrawn = "withdrawn"
)

// maxCommandTries bounds how often a command is taken by runners that do
// not report back.
const maxCommandTries = 5

// CommandEvent carries a command in the deploy.command* events.
type CommandEvent struct {
	ID         string         `json:"id"`
	Deployment string         `json:"deployment,omitempty"` // command
	Action     string         `json:"action,omitempty"`     // command
	Actor      string         `json:"actor,omitempty"`      // command, withdrawn
	Comment    string         `json:"comment,omitempty"`    // command
	Origin     string         `json:"origin,omitempty"`     // command
	Expect     *deploy.Expect `json:"expect,omitempty"`     // command
	Take       string         `json:"take,omitempty"`       // taken
	Runner     string         `json:"runner,omitempty"`     // taken: the runner's name
	Until      int64          `json:"until,omitempty"`      // taken: until when (Unix ms)
	Result     string         `json:"result,omitempty"`     // done: applied, already_applied, not_applied or failed
	Detail     string         `json:"detail,omitempty"`     // done, withdrawn
}

// CommandState is a command as the run's events describe it.
type CommandState struct {
	ID, Deployment, Step, Action, Actor, Comment, Origin string
	Expect                                               deploy.Expect
	State                                                string
	At                                                   int64
	Take, Runner                                         string
	TakenUntil                                           int64
	Tries                                                int
	Result, Detail                                       string
	DoneAt                                               int64
	WithdrawnBy                                          string
}

// open reports whether the command may still be applied.
func (cs *CommandState) open() bool { return cs.State == CommandPending || cs.State == CommandTaken }

// applied reports whether the rollout got the command.
func (cs *CommandState) applied() bool {
	return cs.State == CommandDone && (cs.Result == deploy.Applied || cs.Result == deploy.AlreadyApplied)
}

func (st *RunState) command(id string) *CommandState {
	if id == "" {
		return nil
	}
	for _, cs := range st.Commands {
		if cs.ID == id {
			return cs
		}
	}
	return nil
}

// deploymentStep returns the deploy step that made a deployment.
func (st *RunState) deploymentStep(depID string) (string, *StepState) {
	for _, id := range st.Order {
		if s := st.Steps[id]; s.Dep.Opened() && s.Dep.ID == depID && st.Pipeline.Step(id).IsDeploy() {
			return id, s
		}
	}
	return "", nil
}

// applyControl folds the deploy.command* events.
func (st *RunState) applyControl(ev Event, s *StepState) {
	d := ev.Data.Deploy
	if d == nil || d.Command == nil {
		return
	}
	ce := d.Command
	if ev.Type == DeployCommand {
		cs := &CommandState{ID: ce.ID, Deployment: ce.Deployment, Step: ev.Data.Step, Action: ce.Action, Actor: ce.Actor,
			Comment: ce.Comment, Origin: ce.Origin, State: CommandPending, At: ev.At}
		if ce.Expect != nil {
			cs.Expect = *ce.Expect
		}
		st.Commands = append(st.Commands, cs)
		if ce.Origin == OriginStep && s != nil && s.Dep != nil {
			s.Dep.Command = ce.ID
		}
		return
	}
	cs := st.command(ce.ID)
	if cs == nil {
		return
	}
	switch ev.Type {
	case DeployCommandWithdrawn:
		cs.State, cs.WithdrawnBy, cs.Detail, cs.DoneAt = CommandWithdrawn, ce.Actor, ce.Detail, ev.At
	case DeployCommandTaken:
		cs.State, cs.Take, cs.Runner, cs.TakenUntil = CommandTaken, ce.Take, ce.Runner, ce.Until
		cs.Tries++
	case DeployCommandDone:
		cs.State, cs.Result, cs.Detail, cs.DoneAt = CommandDone, ce.Result, ce.Detail, ev.At
	}
}

// adoptDeployment gives a rollout step the deployment it acts on, as a
// rollback step takes over its deploy step's.
func (st *RunState) adoptDeployment(s *StepState, depID string, at int64) {
	for _, id := range st.Order {
		from := st.Steps[id]
		if from.Dep.Opened() && from.Dep.ID == depID && st.Pipeline.Step(id).IsDeploy() {
			c := *from.Dep
			c.Facts, c.Phase, c.Detail, c.Phases, c.Closed, c.Verdict, c.Command = nil, "", "", 0, false, "", ""
			s.Dep = &c
			break
		}
	}
	if s.Status == StepPending || s.Status.Terminal() {
		s.Attempt++
		s.QueuedAt, s.StartedAt, s.FinishedAt = at, at, 0
		s.Error = ""
	}
}

// ---- giving commands, in a run's transaction ----

// deploymentRow is what the server last observed of a deployment.
type deploymentRow struct {
	snap       deploy.Snapshot
	hash       string
	observedAt int64
	observedBy string
}

func (c *cmd) deploymentRow(depID string) (deploymentRow, error) {
	var r deploymentRow
	var snap string
	err := c.tx.QueryRow(`SELECT snapshot, pod_hash, observed_at, observed_by FROM deployments WHERE id = ?`, depID).Scan(&snap, &r.hash, &r.observedAt, &r.observedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if snap != "" {
		_ = json.Unmarshal([]byte(snap), &r.snap)
	}
	return r, nil
}

// giveCommand records a command and wakes deploy runners.
func (c *cmd) giveCommand(stepID, depID, action, actor, comment, origin string, x deploy.Expect) string {
	id := ids.New("cmd")
	c.emit(DeployCommand, Payload{Step: stepID, Deploy: &DeployEvent{ID: depID, Command: &CommandEvent{ID: id, Deployment: depID,
		Action: action, Actor: actor, Comment: comment, Origin: origin, Expect: &x}}})
	c.after(c.e.notify)
	return id
}

// sameGate reports whether two expectations are the same point of the
// rollout.
func sameGate(a, b deploy.Expect) bool {
	if a.PodHash != b.PodHash {
		return false
	}
	if a.Step == nil || b.Step == nil {
		return a.Step == nil && b.Step == nil
	}
	return *a.Step == *b.Step
}

// duplicate returns a command of the deployment that already does what an
// automatic command would: the same action at the same point, not
// withdrawn and not failed.
func (st *RunState) duplicate(depID, action string, x deploy.Expect) *CommandState {
	for _, cs := range st.Commands {
		if cs.Deployment == depID && cs.Action == action && sameGate(cs.Expect, x) && (cs.open() || cs.applied()) {
			return cs
		}
	}
	return nil
}

// CommandRequest is a person's command on a deployment.
type CommandRequest struct {
	Run, Deployment, Action, Actor, Comment string
}

// Refusal reasons a person's command can meet. The API turns ErrConflict
// into 409 and ErrForbidden into 403.

// commandRefusal says why a person may not give a command now, or nil;
// it also returns what the command would expect.
func (c *cmd) commandRefusal(depID, action, actor string) (string, deploy.Expect, error) {
	st := c.st
	deployID, ds := st.deploymentStep(depID)
	if ds == nil {
		return "", deploy.Expect{}, fmt.Errorf("%w: deployment %s in run %s", ErrNotFound, depID, st.ID)
	}
	def := st.Pipeline.Step(deployID)
	dep := ds.Dep
	switch {
	case def.Rollout == nil:
		return deployID, deploy.Expect{}, fmt.Errorf("%w: %s names no rollout, so there is nothing to %s", ErrConflict, deployID, action)
	case dep.Closed:
		return deployID, deploy.Expect{}, fmt.Errorf("%w: the deployment is over (%s)", ErrConflict, strings.ReplaceAll(dep.Verdict, "_", " "))
	}
	if rb := st.Pipeline.RollbackOf(deployID); rb != nil && action != pipeline.ActionAbort {
		switch st.Steps[rb.ID].Status {
		case StepQueuedS, StepLeasedS, StepRunning, StepWaitingS, StepSucceeded, StepFailed:
			return deployID, deploy.Expect{}, fmt.Errorf("%w: %s is rolling the deployment back", ErrConflict, rb.ID)
		}
	}
	env := c.e.envs[dep.Environment]
	if env.Protected && !c.e.selfOK && action != pipeline.ActionAbort && actor == st.Trigger.Actor {
		return deployID, deploy.Expect{}, fmt.Errorf("%w: %s started this run, and %s is a protected environment, so someone else must %s it (you may still abort it)",
			ErrForbidden, actor, dep.Environment, verbOf(action))
	}
	row, err := c.deploymentRow(depID)
	if err != nil {
		return deployID, deploy.Expect{}, err
	}
	x, why := deploy.Available(action, row.snap, row.hash)
	if why != "" {
		if row.observedAt == 0 {
			why = "no deploy runner has observed the deployment yet"
		}
		return deployID, x, fmt.Errorf("%w: %s", ErrConflict, why)
	}
	for _, cs := range st.Commands {
		if cs.Deployment != depID {
			continue
		}
		switch {
		case cs.open() && cs.Action == action:
			return deployID, x, fmt.Errorf("%w: %s already asked for by %s; a deploy runner has %s", ErrConflict, verbOf(action), whoOf(cs), stateWords(cs))
		case cs.open() && action != pipeline.ActionAbort:
			return deployID, x, fmt.Errorf("%w: %s asked for by %s is still waiting for a deploy runner; withdraw it first, or abort", ErrConflict, verbOf(cs.Action), whoOf(cs))
		case cs.applied() && cs.Action == action && sameGate(cs.Expect, x):
			return deployID, x, fmt.Errorf("%w: %s at this point was already done, asked for by %s", ErrConflict, verbOf(action), whoOf(cs))
		}
	}
	return deployID, x, nil
}

func whoOf(cs *CommandState) string {
	switch cs.Origin {
	case OriginStep:
		return "step " + cs.Step
	case OriginPauses:
		return "step " + cs.Step + " (pauses: promote)"
	}
	return cs.Actor
}

func stateWords(cs *CommandState) string {
	if cs.State == CommandTaken {
		return "taken it"
	}
	return "not taken it yet"
}

func verbOf(action string) string {
	if action == pipeline.ActionPromoteFull {
		return "full promotion"
	}
	return action
}

// RequestCommand gives a person's command to a deployment's rollout. It
// fails with ErrConflict when the rollout is not where the command makes
// sense, or the same command is already given, naming who gave it; with
// ErrForbidden when the person started the run and the environment is
// protected (Promote, Promote full and Retry; Abort is always allowed).
// The caller checks the person's role.
func (e *Engine) RequestCommand(rq CommandRequest) (*CommandView, error) {
	if !e.knows(schemaControl) {
		return nil, fmt.Errorf("%w: this server does not know commands on deployments", ErrConflict)
	}
	rq.Actor, rq.Comment = strings.TrimSpace(rq.Actor), strings.TrimSpace(rq.Comment)
	switch {
	case rq.Actor == "":
		return nil, fmt.Errorf("%w: a command needs someone to give it", ErrInvalid)
	case !slices.Contains(pipeline.RolloutActions, rq.Action):
		return nil, fmt.Errorf("%w: %q is not one of %s", ErrInvalid, rq.Action, strings.Join(pipeline.RolloutActions, ", "))
	case utf8.RuneCountInString(rq.Comment) > MaxCommentLen:
		return nil, fmt.Errorf("%w: the comment is longer than %d characters", ErrInvalid, MaxCommentLen)
	}
	var id string
	err := e.withRun(rq.Run, func(c *cmd) error {
		deployID, x, err := c.commandRefusal(rq.Deployment, rq.Action, rq.Actor)
		if err != nil {
			return err
		}
		// An abort goes first: commands nobody has taken yet are withdrawn.
		if rq.Action == pipeline.ActionAbort {
			for _, cs := range c.st.Commands {
				if cs.Deployment == rq.Deployment && cs.State == CommandPending {
					c.withdraw(cs, rq.Actor, "withdrawn by the abort "+rq.Actor+" asked for")
				}
			}
		}
		id = c.giveCommand(deployID, rq.Deployment, rq.Action, rq.Actor, rq.Comment, OriginPerson, x)
		c.advance()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.command(id)
}

// withdraw records a withdrawal, and fails a rollout step waiting for the
// command.
func (c *cmd) withdraw(cs *CommandState, actor, detail string) {
	c.emit(DeployCommandWithdrawn, Payload{Step: cs.Step, Deploy: &DeployEvent{ID: cs.Deployment,
		Command: &CommandEvent{ID: cs.ID, Actor: actor, Detail: detail}}})
	c.failStepsWaitingFor(cs.ID, fmt.Sprintf("the %s it asked for was withdrawn by %s", verbOf(cs.Action), actor))
}

// failStepsWaitingFor fails the rollout step waiting for a command.
func (c *cmd) failStepsWaitingFor(cmdID, reason string) {
	for _, id := range c.st.Order {
		s := c.st.Steps[id]
		if s.Status == StepWaitingS && s.WaitKind == WaitObserve && s.Dep != nil && s.Dep.Command == cmdID {
			c.emit(StepFinished, Payload{Step: id, Attempt: s.Attempt, Outcome: string(StepFailed), Reason: reason})
		}
	}
}

// WithdrawCommand withdraws a command no deploy runner has taken.
func (e *Engine) WithdrawCommand(runID, cmdID, actor string) error {
	if !e.knows(schemaControl) {
		return fmt.Errorf("%w: this server does not know commands on deployments", ErrConflict)
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return fmt.Errorf("%w: withdrawing needs someone to do it", ErrInvalid)
	}
	return e.withRun(runID, func(c *cmd) error {
		cs := c.st.command(cmdID)
		switch {
		case cs == nil:
			return fmt.Errorf("%w: command %s", ErrNotFound, cmdID)
		case cs.State == CommandTaken:
			return fmt.Errorf("%w: deploy runner %s has taken the %s, so it can no longer be withdrawn", ErrConflict, cs.Runner, verbOf(cs.Action))
		case cs.State != CommandPending:
			return fmt.Errorf("%w: the %s is %s", ErrConflict, verbOf(cs.Action), cs.State)
		}
		c.withdraw(cs, actor, "")
		c.advance()
		return nil
	})
}

// CommandJob is a command a deploy runner applies.
type CommandJob struct {
	ID          string              `json:"id"`
	Take        string              `json:"take"` // names this take; the result must carry it
	Until       int64               `json:"until"`
	Action      string              `json:"action"`
	Expect      deploy.Expect       `json:"expect"`
	Deployment  string              `json:"deployment"`
	RunID       string              `json:"run_id"`
	RunNumber   int                 `json:"run_number"`
	Project     string              `json:"project"`
	Target      string              `json:"target"`
	Application string              `json:"application"`
	Rollout     pipeline.RolloutRef `json:"rollout"`
	Actor       string              `json:"actor"`
	Comment     string              `json:"comment,omitempty"`
}

// CommandResult is what a deploy runner did with a command.
type CommandResult struct {
	Take   string `json:"take"`
	Result string `json:"result"` // applied, already_applied, not_applied or failed
	Detail string `json:"detail"`
}

// TakeCommand hands a deploy runner the oldest command of its targets that
// no runner holds, waiting up to wait for one.
func (e *Engine) TakeCommand(ctx context.Context, r RunnerInfo, targets []string, wait time.Duration) (*CommandJob, error) {
	if !e.knows(schemaControl) {
		return nil, fmt.Errorf("%w: this server does not know commands on deployments", ErrConflict)
	}
	deadline := time.Now().Add(wait)
	for {
		ch := e.waitChan()
		select {
		case <-e.stop:
			return nil, ErrClosed
		default:
		}
		e.touchRunner(r)
		job, err := e.tryTake(r, targets)
		if err != nil || job != nil {
			return job, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil
		}
		if e.db.pg {
			left = min(left, recheckEvery)
		}
		t := time.NewTimer(min(left, e.ttl))
		select {
		case <-ch:
			t.Stop()
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-e.stop:
			t.Stop()
			return nil, ErrClosed
		}
	}
}

func (e *Engine) tryTake(r RunnerInfo, targets []string) (*CommandJob, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	now := e.now().UnixMilli()
	args := []any{now}
	for _, t := range targets {
		args = append(args, t)
	}
	args = append(args, e.schema)
	rows, err := e.db.Query(`SELECT c.id, c.run_id FROM deploy_commands c JOIN runs r ON r.id = c.run_id
		WHERE (c.state = 'pending' OR (c.state = 'taken' AND c.taken_until < ?)) AND c.target IN (`+inList(len(targets))+`)
		AND r.min_schema <= ? ORDER BY c.created_at, c.id LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	type cand struct{ id, run string }
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.run); err != nil {
			rows.Close()
			return nil, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, cd := range cands {
		var job *CommandJob
		err := e.withRun(cd.run, func(c *cmd) error {
			job = nil
			cs := c.st.command(cd.id)
			if cs == nil || !(cs.State == CommandPending || (cs.State == CommandTaken && cs.TakenUntil < c.now)) {
				return nil // taken, withdrawn or done since
			}
			deployID, ds := c.st.deploymentStep(cs.Deployment)
			if ds == nil || !slices.Contains(targets, ds.Dep.Target) {
				return nil
			}
			def := c.st.Pipeline.Step(deployID)
			if cs.Tries >= maxCommandTries || def.Rollout == nil {
				c.commandDone(cs, deploy.CommandFailed, fmt.Sprintf("deploy runners took it %d times without saying what came of it", cs.Tries))
				return nil
			}
			take := ids.New("take")
			until := c.now + c.e.ttl.Milliseconds()
			c.emit(DeployCommandTaken, Payload{Step: cs.Step, Runner: r.ID, Deploy: &DeployEvent{ID: cs.Deployment,
				Command: &CommandEvent{ID: cs.ID, Take: take, Runner: nonEmpty(r.Name, r.ID), Until: until}}})
			job = &CommandJob{ID: cs.ID, Take: take, Until: until, Action: cs.Action, Expect: cs.Expect, Deployment: cs.Deployment,
				RunID: c.st.ID, RunNumber: c.st.Number, Project: c.st.Project, Target: ds.Dep.Target, Application: ds.Dep.Application,
				Rollout: *def.Rollout, Actor: whoOf(cs), Comment: cs.Comment}
			return nil
		})
		if errors.Is(err, ErrNeedsNewer) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if job != nil {
			return job, nil
		}
	}
	return nil, nil
}

// FinishCommand records what a deploy runner did with a command it took.
// A result for a take that is no longer the command's is refused with
// ErrLeaseLost: another runner has it now.
func (e *Engine) FinishCommand(r RunnerInfo, targets []string, res CommandResult) error {
	if !e.knows(schemaControl) {
		return fmt.Errorf("%w: this server does not know commands on deployments", ErrConflict)
	}
	switch res.Result {
	case deploy.Applied, deploy.AlreadyApplied, deploy.NotApplied, deploy.CommandFailed:
	default:
		return fmt.Errorf("%w: result %q", ErrInvalid, res.Result)
	}
	var runID, cmdID, target string
	err := e.db.QueryRow(`SELECT run_id, id, target FROM deploy_commands WHERE take_id = ?`, res.Take).Scan(&runID, &cmdID, &target)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if !slices.Contains(targets, target) {
		return fmt.Errorf("%w: the command is for target %s", ErrForbidden, target)
	}
	return e.withRun(runID, func(c *cmd) error {
		cs := c.st.command(cmdID)
		if cs == nil || cs.State != CommandTaken || cs.Take != res.Take {
			return ErrLeaseLost
		}
		c.commandDone(cs, res.Result, clipText(res.Detail, 1000))
		return nil
	})
}

// commandDone records how a command ended, and what follows: an applied
// abort rejects the approvals that name the deployment; a rollout step
// whose command was not applied fails.
func (c *cmd) commandDone(cs *CommandState, result, detail string) {
	c.emit(DeployCommandDone, Payload{Step: cs.Step, Deploy: &DeployEvent{ID: cs.Deployment,
		Command: &CommandEvent{ID: cs.ID, Result: result, Detail: detail}}})
	switch {
	case cs.Action == pipeline.ActionAbort && (result == deploy.Applied || result == deploy.AlreadyApplied):
		c.rejectLinked(cs.Deployment, "rejected automatically: the rollout was aborted ("+whoOf(cs)+" asked for it)")
	case result == deploy.NotApplied || result == deploy.CommandFailed:
		c.failStepsWaitingFor(cs.ID, fmt.Sprintf("the %s was not applied: %s", verbOf(cs.Action), nonEmpty(detail, result)))
	}
	c.advance()
}

// rejectLinked rejects the approvals waiting that name a deployment.
func (c *cmd) rejectLinked(depID, reason string) {
	deployID, _ := c.st.deploymentStep(depID)
	if deployID == "" {
		return
	}
	for _, id := range c.st.Order {
		s, def := c.st.Steps[id], c.st.Pipeline.Step(id)
		if def.LinksDeployment() && def.Deployment == deployID && s.Status == StepWaitingS && s.WaitKind == "" {
			c.emit(StepRejected, Payload{Step: id, Attempt: s.Attempt, Actor: "launchway", Reason: reason})
		}
	}
}

// waitingLinked reports whether an approval naming the deployment waits.
func (st *RunState) waitingLinked(deployID string) bool {
	for _, id := range st.Order {
		s, def := st.Steps[id], st.Pipeline.Step(id)
		if def.LinksDeployment() && def.Deployment == deployID && s.Status == StepWaitingS {
			return true
		}
	}
	return false
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}

// ---- rollout steps ----

// startRolloutStep starts a rollout step whose needs are met: it takes on
// the deployment and waits for what deploy runners observe; the first fresh
// observation gives its command (see evaluate).
func (c *cmd) startRolloutStep(id string, def *pipeline.Step) {
	d := c.st.Steps[def.Deployment]
	switch {
	case d == nil || !d.Dep.Opened():
		c.failStep(id, "its deployment was never made")
		return
	case d.Dep.Closed:
		c.failStep(id, "its deployment is already over")
		return
	}
	commit := d.Dep.Commit
	if commit == "" && d.Dep.Facts != nil {
		commit = d.Dep.Facts.Commit
	}
	until := def.Until
	if def.Action == pipeline.ActionAbort {
		until = "aborted"
	}
	c.emit(DeployObserving, Payload{Step: id, Deploy: &DeployEvent{ID: d.Dep.ID, Until: until, Commit: commit,
		Deadline: c.now + def.Timeout.D().Milliseconds()}})
	c.after(c.e.notify)
}

// evaluate decides what a snapshot means for a step watching a deployment:
// deploy.Evaluate for deploy and rollback steps; for a rollout step, first
// whether to give its command, then what the rollout did with it. A
// verdict to give a command that the deployment already has is turned into
// waiting.
func (st *RunState) evaluate(stepID string, spec deploy.Spec, snap deploy.Snapshot, hash string, now int64) deploy.Verdict {
	def, s := st.Pipeline.Step(stepID), st.Steps[stepID]
	var v deploy.Verdict
	if !def.IsRolloutStep() {
		v = deploy.Evaluate(spec, snap)
	} else {
		cs := st.command(s.Dep.Command)
		switch {
		case cs == nil:
			v = deploy.StartAction(spec, def.Action, snap, hash)
		case cs.open():
			return deploy.Verdict{Outcome: deploy.Continue, Phase: deploy.PhaseCommand,
				Detail: fmt.Sprintf("waiting for a deploy runner to %s the rollout (it has %s)", deployVerb(cs.Action), stateWords(cs))}
		case cs.State == CommandWithdrawn:
			return deploy.Verdict{Outcome: deploy.Failed, Phase: deploy.PhaseCommand, Detail: fmt.Sprintf("the %s was withdrawn by %s", verbOf(cs.Action), cs.WithdrawnBy)}
		case !cs.applied():
			return deploy.Verdict{Outcome: deploy.Failed, Phase: deploy.PhaseCommand, Detail: fmt.Sprintf("the %s was not applied: %s", verbOf(cs.Action), nonEmpty(cs.Detail, cs.Result))}
		default:
			v = deploy.AfterAction(spec, def.Action, &cs.Expect, snap)
			// A snapshot read before the retry reached the rollout still
			// shows it aborted.
			if v.Outcome == deploy.Failed && def.Action == pipeline.ActionRetry && now-cs.DoneAt < 15_000 {
				v = deploy.Verdict{Outcome: deploy.Continue, Phase: deploy.PhaseRollout, Detail: "waiting for the rollout controller to retry the rollout"}
			}
		}
	}
	if v.Outcome == deploy.Command && v.Expect != nil {
		if dup := st.duplicate(s.Dep.ID, v.Action, *v.Expect); dup != nil {
			v.Outcome = deploy.Continue
			if dup.open() {
				v.Detail += fmt.Sprintf("; %s asked for by %s, waiting for a deploy runner", verbOf(dup.Action), whoOf(dup))
			}
		}
	}
	// Name who aborted the rollout, when it was a command.
	if v.Outcome == deploy.Failed && v.Phase == deploy.PhaseAborted {
		for i := len(st.Commands) - 1; i >= 0; i-- {
			if cs := st.Commands[i]; cs.Deployment == s.Dep.ID && cs.Action == pipeline.ActionAbort && cs.applied() {
				v.Detail += "; aborted by " + whoOf(cs)
				if cs.Comment != "" {
					v.Detail += ": " + cs.Comment
				}
				break
			}
		}
	}
	return v
}

func deployVerb(action string) string {
	if action == pipeline.ActionPromoteFull {
		return "fully promote"
	}
	return action
}

// controlVerdict is how a deployment whose deploy step succeeded ended,
// when rollout steps acted on it: failed if one of them did not succeed or
// the last that did aborted the rollout, abandoned if one was canceled,
// "" if they all did and it was not left aborted.
func (st *RunState) controlVerdict(deployID string) string {
	def := st.Pipeline.Step(deployID)
	steps := st.Pipeline.RolloutStepsOf(deployID)
	if len(steps) == 0 {
		if def.StopsAtPause() {
			return VerdictFailed
		}
		return ""
	}
	last := ""
	for _, r := range steps {
		switch s := st.Steps[r.ID]; s.Status {
		case StepCanceledS:
			return VerdictAbandoned
		case StepSucceeded:
			last = r.Action
		case StepSkippedS:
			if s.SkipKind != "condition" {
				return VerdictFailed
			}
		default:
			return VerdictFailed
		}
	}
	if last == pipeline.ActionAbort || last == "" {
		return VerdictFailed
	}
	return ""
}

// abortOnCancel gives an abort command to each deployment of the run that
// was open before the run was canceled and whose deploy step says
// on_cancel: abort. A deployment whose version was never seen in its
// rollout has nothing to abort.
func (c *cmd) abortOnCancel(open []string, actor string) {
	for _, deployID := range open {
		s := c.st.Steps[deployID]
		def := c.st.Pipeline.Step(deployID)
		if def.OnCancel != "abort" || def.Rollout == nil || !s.Dep.Opened() {
			continue
		}
		row, err := c.deploymentRow(s.Dep.ID)
		if err != nil {
			c.dbFail(err)
			return
		}
		if row.hash == "" {
			continue
		}
		x := deploy.Expect{PodHash: row.hash}
		if ro := row.snap.Rollout; ro != nil && ro.CurrentPodHash == row.hash {
			x.Step = ro.Step
		}
		c.giveCommand(deployID, s.Dep.ID, pipeline.ActionAbort, nonEmpty(actor, "launchway"), "the run was canceled (on_cancel: abort)", OriginOnCancel, x)
	}
}

// openDeployments lists the deploy steps of the run whose deployment is
// open.
func (st *RunState) openDeployments() []string {
	var out []string
	for _, id := range st.Order {
		if s := st.Steps[id]; s.Dep.Opened() && !s.Dep.Closed && st.Pipeline.Step(id).IsDeploy() {
			out = append(out, id)
		}
	}
	return out
}

// ---- persisting ----

// persistControl brings deploy_commands and the schema-14 columns up to
// date with the command's events.
func (c *cmd) persistControl() error {
	st := c.st
	for _, ev := range c.events {
		d := ev.Data.Deploy
		var err error
		switch ev.Type {
		case DeployOpened:
			if def := st.Pipeline.Step(ev.Data.Step); def != nil {
				spec, _ := json.Marshal(deploy.SpecFor(pipeline.KindDeploy, def, ""))
				_, err = c.tx.Exec(`UPDATE deployments SET watch_spec = ? WHERE id = ?`, string(spec), d.ID)
			}
		case DeployCommand:
			ce := d.Command
			target := ""
			if _, ds := st.deploymentStep(ce.Deployment); ds != nil {
				target = ds.Dep.Target
			}
			expect, _ := json.Marshal(ce.Expect)
			_, err = c.tx.Exec(`INSERT INTO deploy_commands(id, deployment, run_id, step_id, action, actor, comment, expect, state, created_at,
					project, target, origin) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				ce.ID, ce.Deployment, st.ID, ev.Data.Step, ce.Action, ce.Actor, ce.Comment, string(expect), CommandPending, ev.At,
				st.Project, target, ce.Origin)
		case DeployCommandWithdrawn:
			_, err = c.tx.Exec(`UPDATE deploy_commands SET state = ?, withdrawn_by = ?, detail = ?, done_at = ? WHERE id = ?`,
				CommandWithdrawn, d.Command.Actor, d.Command.Detail, ev.At, d.Command.ID)
		case DeployCommandTaken:
			_, err = c.tx.Exec(`UPDATE deploy_commands SET state = ?, take_id = ?, taken_by = ?, taken_until = ?, tries = tries + 1 WHERE id = ?`,
				CommandTaken, d.Command.Take, d.Command.Runner, d.Command.Until, d.Command.ID)
		case DeployCommandDone:
			_, err = c.tx.Exec(`UPDATE deploy_commands SET state = ?, result = ?, detail = ?, done_at = ? WHERE id = ?`,
				CommandDone, d.Command.Result, d.Command.Detail, ev.At, d.Command.ID)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ---- reading ----

// CommandView is a command as the run page shows it.
type CommandView struct {
	ID          string        `json:"id"`
	Deployment  string        `json:"deployment"`
	RunID       string        `json:"run_id"`
	Step        string        `json:"step"`
	Action      string        `json:"action"`
	Actor       string        `json:"actor"`
	Comment     string        `json:"comment,omitempty"`
	Origin      string        `json:"origin"`
	Expect      deploy.Expect `json:"expect"`
	State       string        `json:"state"` // pending, taken, done or withdrawn
	Result      string        `json:"result,omitempty"`
	Detail      string        `json:"detail,omitempty"`
	TakenBy     string        `json:"taken_by,omitempty"`
	TakenUntil  int64         `json:"taken_until,omitempty"`
	Tries       int           `json:"tries"`
	WithdrawnBy string        `json:"withdrawn_by,omitempty"`
	At          int64         `json:"at"`
	DoneAt      int64         `json:"done_at,omitempty"`
}

const commandCols = `id, deployment, run_id, step_id, action, actor, comment, origin, expect, state, result, detail, taken_by,
	taken_until, tries, withdrawn_by, created_at, done_at`

func scanCommand(sc interface{ Scan(...any) error }) (CommandView, error) {
	var v CommandView
	var expect string
	err := sc.Scan(&v.ID, &v.Deployment, &v.RunID, &v.Step, &v.Action, &v.Actor, &v.Comment, &v.Origin, &expect, &v.State, &v.Result,
		&v.Detail, &v.TakenBy, &v.TakenUntil, &v.Tries, &v.WithdrawnBy, &v.At, &v.DoneAt)
	if err == nil && expect != "" {
		_ = json.Unmarshal([]byte(expect), &v.Expect)
	}
	return v, err
}

func (e *Engine) command(id string) (*CommandView, error) {
	v, err := scanCommand(e.db.QueryRow(`SELECT `+commandCols+` FROM deploy_commands WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: command %s", ErrNotFound, id)
	}
	return &v, err
}

// addControl adds to deployment views what schema 14 knows: their pod
// template hash, their commands, and who observes their target.
func (e *Engine) addControl(out []DeploymentView) error {
	if !e.knows(schemaControl) || len(out) == 0 {
		return nil
	}
	now := e.now().UnixMilli()
	for i := range out {
		v := &out[i]
		v.Commands = []CommandView{}
		if err := e.db.QueryRow(`SELECT pod_hash FROM deployments WHERE id = ?`, v.ID).Scan(&v.PodHash); err != nil {
			return err
		}
		rows, err := e.db.Query(`SELECT `+commandCols+` FROM deploy_commands WHERE deployment = ? ORDER BY created_at, id`, v.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			cv, err := scanCommand(rows)
			if err != nil {
				rows.Close()
				return err
			}
			v.Commands = append(v.Commands, cv)
		}
		rows.Close()
		var who string
		var until int64
		err = e.db.QueryRow(`SELECT COALESCE(r.name, o.runner), o.expires_at FROM target_observers o LEFT JOIN runners r ON r.id = o.runner
			WHERE o.target = ?`, v.Target).Scan(&who, &until)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if until >= now {
			v.Observer, v.ObserverExpires = who, until
		}
	}
	return nil
}

// Control is whether a command can be given now, and why not.
type Control struct {
	Action    string `json:"action"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // why not, in plain words
	// Forbidden says the reason is who is asking (protected environment),
	// not where the rollout is.
	Forbidden bool `json:"forbidden,omitempty"`
}

// Controls says, for each command, whether actor could give it to a
// deployment now, as far as the deployment and the protected-environment
// rule decide; the caller adds the person's role.
func (e *Engine) Controls(runID, depID, actor string) (env Environment, out []Control, err error) {
	if !e.knows(schemaControl) {
		return env, nil, fmt.Errorf("%w: this server does not know commands on deployments", ErrConflict)
	}
	err = e.readRun(runID, func(c *cmd) error {
		_, ds := c.st.deploymentStep(depID)
		if ds == nil {
			return fmt.Errorf("%w: deployment %s in run %s", ErrNotFound, depID, runID)
		}
		env = c.e.envs[ds.Dep.Environment]
		for _, a := range pipeline.RolloutActions {
			ctl := Control{Action: a, Available: true}
			if _, _, err := c.commandRefusal(depID, a, actor); err != nil {
				switch {
				case errors.Is(err, ErrForbidden):
					ctl.Available, ctl.Forbidden, ctl.Reason = false, true, strings.TrimPrefix(err.Error(), ErrForbidden.Error()+": ")
				case errors.Is(err, ErrConflict):
					ctl.Available, ctl.Reason = false, strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")
				default:
					return err
				}
			}
			out = append(out, ctl)
		}
		return nil
	})
	return env, out, err
}

// readRun folds a run and calls fn with a command whose transaction is
// rolled back: for reading what a command would decide, without changing
// anything.
func (e *Engine) readRun(runID string, fn func(c *cmd) error) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	evs, _, err := loadEvents(tx, runID)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		return fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	st := e.fold(evs)
	if err := needsNewer(st); err != nil {
		return err
	}
	return fn(&cmd{e: e, st: st, tx: tx, now: e.now().UnixMilli(), runID: runID})
}

// EnvironmentOf returns the environment a deployment or a linked approval
// of a run is about, for the API's access checks: "" when the step (or
// deployment) is not about one.
func (e *Engine) EnvironmentOf(runID, stepID, depID string) (string, error) {
	if !e.knows(schemaDeploy) {
		return "", nil
	}
	env := ""
	err := e.readRun(runID, func(c *cmd) error {
		if depID != "" {
			if _, ds := c.st.deploymentStep(depID); ds != nil {
				env = ds.Dep.Environment
			} else {
				return fmt.Errorf("%w: deployment %s in run %s", ErrNotFound, depID, runID)
			}
			return nil
		}
		def := c.st.Pipeline.Step(stepID)
		if def == nil {
			return nil
		}
		if def.LinksDeployment() {
			if d := c.st.Pipeline.Step(def.Deployment); d != nil {
				env = d.Environment
			}
		}
		return nil
	})
	return env, err
}
