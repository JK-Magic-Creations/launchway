package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxCommentLen bounds the comment a person may give with a decision.
const MaxCommentLen = 2000

// Decide approves or rejects an approval step that is waiting. Approving
// lets the steps that need it go ahead; rejecting fails it, so they are
// skipped and the run fails (a resume makes it wait again).
//
// It fails with ErrConflict if the step is not waiting, including when its
// deadline has passed: from then on only expiry decides, so a decision and
// an expiry racing each other can never both happen.
func (e *Engine) Decide(runID, stepID string, approve bool, actor, comment string) error {
	actor, comment = strings.TrimSpace(actor), strings.TrimSpace(comment)
	if actor == "" {
		return fmt.Errorf("%w: a decision needs someone to make it", ErrInvalid)
	}
	if utf8.RuneCountInString(comment) > MaxCommentLen {
		return fmt.Errorf("%w: the comment is longer than %d characters", ErrInvalid, MaxCommentLen)
	}
	return e.withRun(runID, func(c *cmd) error {
		def := c.st.Pipeline.Step(stepID)
		if def == nil {
			return fmt.Errorf("%w: step %s", ErrNotFound, stepID)
		}
		if !def.IsApproval() {
			return fmt.Errorf("%w: %s is not an approval step", ErrConflict, stepID)
		}
		s := c.st.Steps[stepID]
		switch {
		case s.Status != StepWaitingS:
			return fmt.Errorf("%w: %s is not waiting for a decision (it is %s)", ErrConflict, stepID, s.Status)
		case c.now >= s.Deadline:
			return fmt.Errorf("%w: %s is no longer waiting: nobody decided within %s", ErrConflict, stepID, def.Timeout.Short())
		case approve && !c.e.selfOK && actor == c.st.Trigger.Actor:
			return fmt.Errorf("%w: %s started this run, so someone else must approve it (you may still reject it)", ErrForbidden, actor)
		}
		p := Payload{Step: stepID, Attempt: s.Attempt, Actor: actor, Comment: comment}
		if approve {
			c.emit(StepApproved, p)
		} else {
			p.Reason = "rejected by " + actor
			if comment != "" {
				p.Reason += ": " + comment
			}
			c.emit(StepRejected, p)
		}
		c.advance()
		return nil
	})
}

// expireWaits ends the waits whose deadline has passed: it rejects
// approvals nobody decided, and fails deploy and rollback steps that did not
// get done in time (queued for a deploy runner, or waiting for what deploy
// runners observe). Every server does this; each step is ended exactly
// once, because the ending is an event and two servers cannot both append
// the same run_seq. The loser retries, finds the step no longer waiting, and
// stops.
func (e *Engine) expireWaits() {
	now := e.now().UnixMilli()
	q, args := `SELECT run_id, step_id FROM steps WHERE status = ? AND deadline <= ?`, []any{StepWaitingS, now}
	switch {
	case e.knows(schemaDeploy):
		q = `SELECT s.run_id, s.step_id FROM steps s JOIN runs r ON r.id = s.run_id
			WHERE s.deadline > 0 AND s.deadline <= ? AND (s.status = ? OR (s.status = ? AND s.target <> '')) AND r.min_schema <= ?`
		args = []any{now, StepWaitingS, StepQueuedS, e.schema}
	case e.knows(schemaReruns):
		// Runs that need a newer server are left to one.
		q = `SELECT s.run_id, s.step_id FROM steps s JOIN runs r ON r.id = s.run_id
			WHERE s.status = ? AND s.deadline <= ? AND r.min_schema <= ?`
		args = append(args, e.schema)
	}
	rows, err := e.db.Query(q, args...)
	if err != nil {
		e.log.Error("looking for expired approvals", "err", err)
		return
	}
	type due struct{ runID, stepID string }
	var found []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.runID, &d.stepID); err != nil {
			rows.Close()
			e.log.Error("looking for expired approvals", "err", err)
			return
		}
		found = append(found, d)
	}
	rows.Close()
	for _, d := range found {
		err := e.withRun(d.runID, func(c *cmd) error {
			s := c.st.Steps[d.stepID]
			def := c.st.Pipeline.Step(d.stepID)
			if s == nil || def == nil || s.Deadline == 0 || c.now < s.Deadline {
				return nil // decided, canceled or expired since the query above
			}
			if def.IsDeployKind() {
				return c.expireDeploy(d.stepID, def)
			}
			if s.Status != StepWaitingS || !def.IsApproval() {
				return nil
			}
			c.emit(StepRejected, Payload{Step: d.stepID, Attempt: s.Attempt,
				Reason: "nobody decided within " + def.Timeout.Short()})
			c.advance()
			return nil
		})
		if err != nil && !errors.Is(err, ErrNeedsNewer) {
			e.log.Error("ending a wait whose deadline passed", "run", d.runID, "step", d.stepID, "err", err)
		}
	}
}

// Approval is an approval step waiting for someone to decide.
type Approval struct {
	RunID         string `json:"run_id"`
	Project       string `json:"project"`
	RunNumber     int    `json:"run_number"`
	StepID        string `json:"step_id"`
	Message       string `json:"message"`
	StartedBy     string `json:"started_by"`     // who started the run
	CommitSubject string `json:"commit_subject"` // the first line of the commit message
	ShortSHA      string `json:"short_sha"`
	Branch        string `json:"branch"`
	Tag           string `json:"tag,omitempty"`
	PullRequest   int    `json:"pull_request,omitempty"` // for a run of a pull request
	WaitingSince  int64  `json:"waiting_since"`
	Deadline      int64  `json:"deadline"`
}

// Approvals returns every approval waiting for a decision, across all runs,
// the most recent first.
func (e *Engine) Approvals() ([]Approval, error) {
	onlyApprovals := ""
	if e.knows(schemaDeploy) {
		onlyApprovals = ` AND s.wait_kind = ''` // deploy steps wait too, for other things
	}
	rows, err := e.db.Query(`SELECT s.run_id, r.project, r.number, s.step_id, r.actor, r.message, r.sha, r.branch, r.tag,
			r.event, r.ref, s.queued_at, s.deadline
		FROM steps s JOIN runs r ON r.id = s.run_id
		WHERE s.status = ?`+onlyApprovals+`
		ORDER BY s.queued_at DESC, s.run_id DESC, s.position`, StepWaitingS)
	if err != nil {
		return nil, err
	}
	out := []Approval{}
	for rows.Next() {
		var a Approval
		var sha, event, ref string
		if err := rows.Scan(&a.RunID, &a.Project, &a.RunNumber, &a.StepID, &a.StartedBy, &a.CommitSubject, &sha,
			&a.Branch, &a.Tag, &event, &ref, &a.WaitingSince, &a.Deadline); err != nil {
			rows.Close()
			return nil, err
		}
		a.PullRequest = pullRequestNumber(event, ref)
		a.ShortSHA = sha[:min(len(sha), 8)]
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	// The messages are in the pipelines, which are kept with each run.
	marks, args, seen := []string{}, []any{RunCreated}, map[string]bool{}
	for _, a := range out {
		if !seen[a.RunID] {
			seen[a.RunID] = true
			marks, args = append(marks, "?"), append(args, a.RunID)
		}
	}
	prow, err := e.db.Query(`SELECT run_id, data FROM events WHERE type = ? AND run_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	messages := map[string]string{} // run/step → message
	for prow.Next() {
		var runID, data string
		if err := prow.Scan(&runID, &data); err != nil {
			return nil, err
		}
		var created Payload
		if err := json.Unmarshal([]byte(data), &created); err != nil {
			return nil, fmt.Errorf("run %s: %w", runID, err)
		}
		for _, s := range created.Pipeline.Steps {
			if s.IsApproval() {
				messages[runID+"/"+s.ID] = s.Message
			}
		}
	}
	if err := prow.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Message = messages[out[i].RunID+"/"+out[i].StepID]
	}
	return out, nil
}
