package engine

// Approvals that ask who may decide and how many distinct people must
// approve (schema 27): approvers: { groups, users, count } on the step, and
// min_approvals on a protected environment, which the step cannot lower.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// ApprovalRecord is one person's approval of an approval step.
type ApprovalRecord struct {
	Actor   string `json:"actor"`
	At      int64  `json:"at"`
	Comment string `json:"comment,omitempty"`
}

// Decider is who decides: the name the sign-in gave and the groups the
// identity provider gave them. Authorisation by approvers is judged here, in
// the engine, so the API, MCP and any other client cannot differ.
type Decider struct {
	Actor  string
	Groups []string
}

// Decision is what a decision came to: the approvals the step has now, how
// many it needs, and whether it is decided.
type Decision struct {
	Approvals  []ApprovalRecord `json:"approvals"`
	Needed     int              `json:"approvals_needed"`
	NeededFrom string           `json:"approvals_needed_from,omitempty"`
	Done       bool             `json:"done"`
}

// applyApproval applies step.approved, step.rejected and
// step.approval_recorded.
func (st *RunState) applyApproval(s *StepState, ev Event) {
	p := ev.Data
	if ev.Type == StepApprovalRecorded {
		s.Approvals = append(s.Approvals, ApprovalRecord{Actor: p.Actor, At: ev.At, Comment: p.Comment})
		return
	}
	st.endApprovalWait(s, ev.At)
	if ev.Type == StepApproved {
		s.Status = StepSucceeded
		s.Error = ""
		s.Approvals = append(s.Approvals, ApprovalRecord{Actor: p.Actor, At: ev.At, Comment: p.Comment})
	} else {
		s.Status = StepFailed
		s.Failures++
		s.Error = p.Reason
	}
	s.FinishedAt = ev.At
	s.DecidedBy, s.DecidedAt, s.Comment = p.Actor, ev.At, p.Comment
}

// approvalWait is the step.waiting of an approval: its deadline, and, when it
// asks for more than one approval, how many and why.
func (c *cmd) approvalWait(id string, def *pipeline.Step, attempt int) Payload {
	p := Payload{Step: id, Attempt: attempt, Deadline: c.now + def.Timeout.D().Milliseconds()}
	if n, from := c.approvalsNeeded(def); n > 1 {
		p.Needed, p.NeededFrom, p.Requires = n, from, schemaParity2
	}
	return p
}

// approvalsNeeded is how many distinct people must approve: the larger of the
// step's count and the min_approvals of the protected environment its
// deployment goes to, so whoever edits a pipeline cannot weaken production.
func (c *cmd) approvalsNeeded(def *pipeline.Step) (int, string) {
	n, from := 1, ""
	if def.Approvers != nil && def.Approvers.Count > 1 {
		n, from = def.Approvers.Count, "the step's approvers.count"
	}
	if dep := c.st.Pipeline.Step(def.Deployment); dep != nil && dep.Environment != "" {
		if env := c.e.live().envs[dep.Environment]; env.Protected && env.MinApprovals > n {
			n, from = env.MinApprovals, "the min_approvals of the environment "+env.Name
		}
	}
	return n, from
}

// decideApproval makes a decision on an approval step that is waiting.
func (c *cmd) decideApproval(stepID string, def *pipeline.Step, approve bool, who Decider, comment string) (*Decision, error) {
	s := c.st.Steps[stepID]
	actor := who.Actor
	switch {
	case s.Status != StepWaitingS:
		return nil, fmt.Errorf("%w: %s is not waiting for a decision (it is %s)", ErrConflict, stepID, s.Status)
	case c.now >= s.Deadline:
		return nil, fmt.Errorf("%w: %s is no longer waiting: nobody decided within %s", ErrConflict, stepID, def.Timeout.Short())
	case def.Approvers.Restricts() && !def.Approvers.Allows(actor, who.Groups):
		return nil, fmt.Errorf("%w: only %s may decide %s", ErrForbidden, def.Approvers.Describe(), stepID)
	case approve && !c.st.selfApproval(c.e) && actor == c.st.Trigger.Actor:
		return nil, fmt.Errorf("%w: %s started this run, so someone else must approve it (you may still reject it)", ErrForbidden, actor)
	case approve && slices.ContainsFunc(s.Approvals, func(r ApprovalRecord) bool { return r.Actor == actor }):
		return nil, fmt.Errorf("%w: %s has already approved %s; another person must approve it", ErrConflict, actor, stepID)
	}
	p := Payload{Step: stepID, Attempt: s.Attempt, Actor: actor, Comment: comment}
	if !approve {
		p.Reason = "rejected by " + actor
		if comment != "" {
			p.Reason += ": " + comment
		}
		c.emit(StepRejected, p)
		c.advance()
		return c.decisionOf(stepID, true), nil
	}
	needed, _ := c.approvalsNeeded(def)
	needed = max(needed, s.Needed, 1)
	if len(s.Approvals)+1 < needed {
		p.Requires = schemaParity2
		c.emit(StepApprovalRecorded, p)
		return c.decisionOf(stepID, false), nil
	}
	c.emit(StepApproved, p)
	c.advance()
	return c.decisionOf(stepID, true), nil
}

func (c *cmd) decisionOf(stepID string, done bool) *Decision {
	s := c.st.Steps[stepID]
	d := &Decision{Approvals: slices.Clone(s.Approvals), Needed: max(s.Needed, 1), NeededFrom: s.NeededFrom, Done: done}
	if d.Approvals == nil {
		d.Approvals = []ApprovalRecord{}
	}
	return d
}

// DecideAs is Decide for a person who is known by their groups too: an
// approval with approvers is judged by their name and groups. It returns how
// the approval stands afterwards.
func (e *Engine) DecideAs(runID, stepID string, approve bool, who Decider, comment string) (*Decision, error) {
	who.Actor, comment = strings.TrimSpace(who.Actor), strings.TrimSpace(comment)
	if who.Actor == "" {
		return nil, fmt.Errorf("%w: a decision needs someone to make it", ErrInvalid)
	}
	if utf8.RuneCountInString(comment) > MaxCommentLen {
		return nil, fmt.Errorf("%w: the comment is longer than %d characters", ErrInvalid, MaxCommentLen)
	}
	var out *Decision
	err := e.withRun(runID, func(c *cmd) error {
		def := c.st.Pipeline.Step(stepID)
		if def == nil {
			return fmt.Errorf("%w: step %s", ErrNotFound, stepID)
		}
		if def.IsVerify() && c.e.knows(schemaVerify) {
			return c.markVerifyByPerson(stepID, def, approve, who.Actor, comment)
		}
		if !def.IsApproval() {
			return fmt.Errorf("%w: %s is not an approval step", ErrConflict, stepID)
		}
		var err error
		out, err = c.decideApproval(stepID, def, approve, who, comment)
		return err
	})
	return out, err
}

// approvalProgress is what a step row keeps of an approval's progress, as
// JSON in steps.approvals, for views and the list of waiting approvals.
type approvalProgress struct {
	Needed     int              `json:"needed,omitempty"`
	NeededFrom string           `json:"needed_from,omitempty"`
	Approvals  []ApprovalRecord `json:"approvals,omitempty"`
}

func (a approvalProgress) text() string {
	if a.Needed == 0 && len(a.Approvals) == 0 {
		return ""
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// persistParity2 writes what schema 27 keeps on the rows of the steps that
// changed.
func (c *cmd) persistParity2(changed []string) error {
	for _, id := range changed {
		s := c.st.Steps[id]
		prog := approvalProgress{Needed: s.Needed, NeededFrom: s.NeededFrom, Approvals: s.Approvals}.text()
		if _, err := c.tx.Exec(`UPDATE steps SET approvals = ? WHERE run_id = ? AND step_id = ?`, prog, c.st.ID, id); err != nil {
			return err
		}
	}
	return nil
}

// readApprovals reads steps.approvals into a step view or an approval.
func readApprovals(text string) approvalProgress {
	var a approvalProgress
	if text != "" {
		_ = json.Unmarshal([]byte(text), &a)
	}
	return a
}

// setProgress fills an approval's progress from steps.approvals.
func (a *Approval) setProgress(text string) {
	p := readApprovals(text)
	a.Approvals, a.ApprovalsNeeded, a.ApprovalsNeededFrom = p.Approvals, max(p.Needed, 1), p.NeededFrom
	if a.Approvals == nil {
		a.Approvals = []ApprovalRecord{}
	}
}

// setApprovals fills a step view's progress from steps.approvals.
func (v *StepView) setApprovals(text string) {
	p := readApprovals(text)
	v.Approvals, v.ApprovalsNeededFrom = p.Approvals, p.NeededFrom
	if p.Needed > 1 {
		v.ApprovalsNeeded = p.Needed
	}
}

// checkMinApprovals checks an environment's min_approvals: a floor of at most
// pipeline.MaxApprovers, which only a protected environment has.
func checkMinApprovals(env Environment) error {
	switch {
	case env.MinApprovals < 0 || env.MinApprovals > pipeline.MaxApprovers:
		return fmt.Errorf("environment %s: min_approvals is between 0 and %d", env.Name, pipeline.MaxApprovers)
	case env.MinApprovals > 1 && !env.Protected:
		return fmt.Errorf("environment %s: min_approvals needs protected: true, because only a protected environment has approvals that guard its deployments", env.Name)
	}
	return nil
}
