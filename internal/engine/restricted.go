package engine

// Restricted runs (schema 37): a pull request from an automation account or a
// fork runs code nobody on the team reviewed, so by default it runs in a
// restricted mode: no secrets, no deploy steps, no cache saved, nothing pushed
// to a registry, and a pipeline taken from the default branch (the source
// package does that). A maintainer who has looked at the change can approve the
// run ("Run with secrets"); that is recorded, and lifts the restrictions for
// the steps that have not started yet.

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Codes of why a run is restricted.
const (
	RestrictAutomation = "automation_account"
	RestrictFork       = "fork"
)

// SkipRestricted is the skip kind of a step a restricted run does not run.
const SkipRestricted = "restricted"

// RestrictedEvent says a run is restricted, and why in words.
type RestrictedEvent struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// RestrictionState is a run's restriction as its events leave it.
type RestrictionState struct {
	Code, Reason string
	Approved     bool
	By, Comment  string
	At           int64
}

// Restriction is a restricted run as shown with the run.
type Restriction struct {
	Code     string `json:"code"`
	Reason   string `json:"reason"`
	Approved bool   `json:"approved"`
	By       string `json:"approved_by,omitempty"`
	At       int64  `json:"approved_at,omitempty"`
	// Summary is the sentence the run page shows.
	Summary string `json:"summary"`
}

// restricted reports whether the run's restrictions are in force.
func (st *RunState) restricted() bool { return st.Restriction != nil && !st.Restriction.Approved }

func (st *RunState) restrictionView() *Restriction {
	r := st.Restriction
	if r == nil {
		return nil
	}
	v := &Restriction{Code: r.Code, Reason: r.Reason, Approved: r.Approved, By: r.By, At: r.At}
	if r.Approved {
		v.Summary = fmt.Sprintf("restricted (%s), approved by %s to run with secrets", r.Reason, r.By)
	} else {
		v.Summary = "restricted: " + r.Reason + ": it runs without secrets, deploys, caches or registry pushes until a maintainer approves it"
	}
	return v
}

// applyTrusted folds run.trusted.
func (st *RunState) applyTrusted(ev Event) {
	if r := st.Restriction; r != nil {
		r.Approved, r.By, r.Comment, r.At = true, ev.Data.Actor, ev.Data.Comment, ev.At
	}
}

func (t EventType) isTrust() bool { return t == RunTrusted }

// restrictedSkip is why a restricted run does not run a step, or "".
func (st *RunState) restrictedSkip(def *pipeline.Step) string {
	if st.restricted() && def != nil && def.IsDeployKind() {
		return "restricted: this run is for a " + st.Restriction.Reason + ", so it deploys nothing until a maintainer approves it"
	}
	return ""
}

// TrustRun is a maintainer's "Run with secrets": the restrictions of the run
// are lifted for the steps that have not started, and the decision is recorded.
func (e *Engine) TrustRun(runID, actor, comment string) error {
	if !e.knows(schemaQueues) {
		return fmt.Errorf("%w: this server does not know restricted runs (schema %d)", ErrConflict, schemaQueues)
	}
	return e.withRun(runID, func(c *cmd) error {
		if !c.st.restricted() || c.st.Status.Terminal() {
			return fmt.Errorf("%w: the run is not restricted, or it is over", ErrConflict)
		}
		c.emit(RunTrusted, Payload{Actor: actor, Comment: comment, Requires: schemaQueues})
		c.advance()
		return nil
	})
}

// trustAction is the action that approves a restricted run.
func (st *RunState) trustAction() []Action {
	if st.Restriction == nil {
		return nil
	}
	a := Action{Name: "run_with_secrets", Who: "operator", Available: st.restricted() && !st.Status.Terminal(),
		Risk: "The run's steps that have not started may read secrets and deploy: approve only after looking at what the pull request changes."}
	if !a.Available {
		a.Reason = "the run is already approved, or over"
	}
	return []Action{a}
}

// restrictedFor is what run.created records of a run's restriction: what the
// caller found, if the installation restricts (the source package decides by
// the project's pull_requests.untrusted setting).
func (nr *NewRun) restrictedFor(e *Engine) *RestrictedEvent {
	if nr.Restricted == nil || nr.Trigger.Event != pipeline.EventPullRequest || !e.knows(schemaQueues) {
		return nil
	}
	return nr.Restricted
}

// restrictedEvent is a run's restriction as run.created records it, for a re-run
// (which is as restricted as the run it repeats, and not approved).
func (st *RunState) restrictedEvent() *RestrictedEvent {
	if st.Restriction == nil {
		return nil
	}
	return &RestrictedEvent{Code: st.Restriction.Code, Reason: st.Restriction.Reason}
}
