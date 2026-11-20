package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/ids"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Re-running from a step. A re-run is a new run of a finished one, at the
// same commit with the same graph, libraries (at the commits the original
// recorded) and trigger. The step it starts from and every step after it
// (that needs it, directly or not) run again; every other step is reused:
// recorded as it ended in the original, with its outputs, by a step.reused
// event, and never handed to a runner. So steps that run again receive the
// outputs of reused steps exactly as they were.
//
// Files that reused steps wrote to the workspace are not there: a re-run
// starts from a fresh checkout. Steps that need such files must be re-run.
//
// Resume is different: it continues the same run, running again only the
// steps that did not succeed.

// RerunRequest asks for a re-run.
type RerunRequest struct {
	Run   string // the run to re-run; it must have finished
	From  string // the step to start from
	Actor string // who asked: the re-run's trigger names them
	// DraftSecrets, for a draft run only, says whether the re-run's steps
	// may read secrets, as the configuration says now.
	DraftSecrets *bool
}

// RerunPlan says what a re-run from a step would do.
type RerunPlan struct {
	Run   []string `json:"run"`   // the step and everything after it, in pipeline order
	Reuse []string `json:"reuse"` // every other step
}

// after returns the step and every step that needs it, directly or not.
func after(p *pipeline.Pipeline, from string) map[string]bool {
	out := map[string]bool{from: true}
	for changed := true; changed; {
		changed = false
		for _, s := range p.Steps {
			if out[s.ID] {
				continue
			}
			for _, n := range s.Needs {
				if out[n] {
					out[s.ID], changed = true, true
					break
				}
			}
		}
	}
	return out
}

// before returns every step the step needs, directly or not.
func before(p *pipeline.Pipeline, from string) map[string]bool {
	out := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		def := p.Step(id)
		if def == nil {
			return
		}
		for _, n := range def.Needs {
			if !out[n] {
				out[n] = true
				walk(n)
			}
		}
	}
	walk(from)
	return out
}

// planRerun works out a re-run of a finished run from a step, or explains
// why there can be none.
func planRerun(orig *RunState, from string) (RerunPlan, error) {
	plan := RerunPlan{Run: []string{}, Reuse: []string{}}
	if orig.Pipeline.Step(from) == nil {
		return plan, fmt.Errorf("%w: run #%d has no step %s", ErrNotFound, orig.Number, from)
	}
	again := after(orig.Pipeline, from)
	needed := before(orig.Pipeline, from)
	var upstream, others []string
	for _, id := range orig.Order {
		if again[id] {
			plan.Run = append(plan.Run, id)
			continue
		}
		plan.Reuse = append(plan.Reuse, id)
		s := orig.Steps[id]
		if s.Status == StepSucceeded || (s.Status == StepSkippedS && s.SkipKind == "condition") {
			continue
		}
		desc := fmt.Sprintf("%s (%s)", id, s.Status)
		if needed[id] {
			upstream = append(upstream, desc)
		} else {
			others = append(others, desc)
		}
	}
	// Deployments: a re-run never reuses a deploy or rollback step, and
	// starts a deployment from its deploy step.
	for _, s := range orig.Pipeline.Steps {
		if s.IsDeploy() && s.ID != from && slices.Contains(orig.Pipeline.DeploymentScope(s.ID), from) {
			return plan, fmt.Errorf("%w: cannot re-run from %s: it is part of deployment %s, which must be made again; re-run from %s",
				ErrConflict, from, s.ID, s.ID)
		}
	}
	for _, id := range plan.Reuse {
		if def := orig.Pipeline.Step(id); def.IsDeployKind() && orig.Steps[id].Status != StepSkippedS {
			return plan, fmt.Errorf("%w: cannot re-run from %s: deploy step %s would be reused, and deploy steps always run again; "+
				"re-run from %s or a step it needs", ErrConflict, from, id, id)
		}
	}
	switch {
	case len(upstream) > 0:
		return plan, fmt.Errorf("%w: cannot re-run from %s: it needs %s, which did not succeed in run #%d, so there is nothing to reuse. "+
			"Re-run from an earlier step, or resume the run", ErrConflict, from, strings.Join(upstream, ", "), orig.Number)
	case len(others) > 0:
		return plan, fmt.Errorf("%w: cannot re-run from %s: %s did not succeed in run #%d and does not come after %s, so it can be neither reused nor run again. "+
			"Re-run from a step it needs, or resume the run", ErrConflict, from, strings.Join(others, ", "), orig.Number, from)
	}
	return plan, nil
}

// PlanRerun says what re-running a run from a step would run and reuse, or
// why it cannot be re-run from there.
func (e *Engine) PlanRerun(runID, from string) (RerunPlan, error) {
	orig, err := e.finishedRun(runID)
	if err != nil {
		return RerunPlan{Run: []string{}, Reuse: []string{}}, err
	}
	return planRerun(orig, from)
}

// finishedRun reads a run that may be re-run: one this server understands
// fully, that has finished.
func (e *Engine) finishedRun(runID string) (*RunState, error) {
	if !e.knows(schemaReruns) {
		return nil, fmt.Errorf("%w: this server does not know re-runs (schema %d)", ErrConflict, schemaReruns)
	}
	evs, _, err := loadEvents(e.db, runID)
	if err != nil {
		return nil, err
	}
	orig := e.fold(evs)
	if orig == nil {
		return nil, fmt.Errorf("%w: run %s", ErrNotFound, runID)
	}
	if err := needsNewer(orig); err != nil {
		return nil, err
	}
	if !orig.Status.Terminal() {
		return nil, fmt.Errorf("%w: run #%d is %s; only a finished run can be re-run", ErrConflict, orig.Number, orig.Status)
	}
	return orig, nil
}

// Rerun creates a re-run of a finished run, from a step (see the top of
// this file). It refuses if a step that would be reused did not succeed
// (or was not skipped by its condition) in the original. Approvals after
// the step wait for a decision again; approvals before it are reused with
// the decision made in the original. A draft run's re-run is a draft run
// of the same text.
func (e *Engine) Rerun(rq RerunRequest) (*RunSummary, error) {
	orig, err := e.finishedRun(rq.Run)
	if err != nil {
		return nil, err
	}
	plan, err := planRerun(orig, rq.From)
	if err != nil {
		return nil, err
	}
	var draft *Draft
	switch {
	case orig.Draft != nil && rq.DraftSecrets == nil:
		return nil, fmt.Errorf("%w: run #%d is a draft run: say whether its steps may read secrets", ErrConflict, orig.Number)
	case orig.Draft == nil && rq.DraftSecrets != nil:
		return nil, fmt.Errorf("%w: run #%d is not a draft run", ErrConflict, orig.Number)
	case orig.Draft != nil:
		draft = &Draft{Text: orig.Draft.Text, Hash: orig.Draft.Hash, Secrets: *rq.DraftSecrets}
	}
	if err := checkLibraries(orig.Pipeline, orig.Source); err != nil {
		return nil, err
	}
	// The same trigger, but started by whoever asked, so that approvals
	// after the step are not theirs to approve if self-approval is off.
	trig, src := orig.Trigger, orig.Source
	trig.Actor = rq.Actor
	rerun := &Rerun{Of: orig.ID, OfNumber: orig.Number, From: rq.From}
	runID := ids.New("run")
	err = e.runTx(runID, true, func(c *cmd) error {
		var number int
		if err := c.tx.QueryRow(`INSERT INTO run_counters(project, last) VALUES(?, 1)
			ON CONFLICT(project) DO UPDATE SET last = run_counters.last + 1 RETURNING last`, orig.Project).Scan(&number); err != nil {
			return err
		}
		var prSecrets *bool
		if trig.Event == pipeline.EventPullRequest {
			// A pull-request run's re-run may read secrets only if the
			// original could: it runs the same code.
			allowed := orig.PullRequestSecrets
			prSecrets = &allowed
		}
		poolRequires := 0
		if e.knows(schemaPools) && e.buildPoolsConfigured() {
			poolRequires = schemaPools // as in CreateRun
		}
		c.emit(RunCreated, Payload{Project: orig.Project, Number: number, Pipeline: c.e.withStoreRequires(orig.Pipeline), Trigger: &trig, Source: &src,
			Draft: draft, Rerun: rerun, PullRequestSecrets: prSecrets, SelfApproval: c.e.selfApprovalNow(), Requires: poolRequires})
		for _, id := range plan.Reuse {
			s := orig.Steps[id]
			from := &Reused{Run: orig.ID, Number: orig.Number, Attempt: s.Attempt, DecidedAt: s.DecidedAt}
			if s.ReusedFrom != "" {
				from.Run, from.Number, from.Attempt = s.ReusedFrom, s.ReusedNumber, s.ReusedAttempt
			}
			p := Payload{Step: id, Outcome: string(s.Status), Outputs: s.Outputs, Reused: from, Actor: s.DecidedBy, Comment: s.Comment}
			if s.Status == StepSkippedS {
				p.SkipKind, p.Reason = s.SkipKind, s.Error
			}
			c.emit(StepReused, p)
		}
		c.advance()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return e.runSummary(runID)
}
