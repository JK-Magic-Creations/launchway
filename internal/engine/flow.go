package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Flow control (schema 24; see the pipeline package's flow.go): what the
// engine does for conditions on step outputs and for failures a step
// tolerates.

// tolerates reports whether a step.finished that ends in failure is one its
// pipeline lets the run go on from (on_failure: continue). A failure that is
// about to be retried is not final, and a run that is being canceled
// tolerates nothing: the step's failure is the cancel's doing.
func (c *cmd) tolerates(p *Payload) bool {
	if p.Outcome != string(StepFailed) || p.retrying || !c.e.knows(schemaFlow) || c.st.CancelRequested {
		return false
	}
	def := c.st.Pipeline.Step(p.Step)
	return def != nil && def.OnFailure == pipeline.OnFailureContinue
}

// condContext is what the conditions of a step are tested against: the
// run's trigger, and the outputs steps have written so far.
func (c *cmd) condContext(ctx pipeline.Context) pipeline.Context {
	ctx.Output = func(step, key string) (string, bool) {
		s := c.st.Steps[step]
		if s == nil {
			return "", false
		}
		v, ok := s.Outputs[key]
		return v, ok
	}
	return ctx
}

// persistFlow writes what schema 24 keeps in the steps and runs tables:
// whether a failed step was allowed to fail, when a retry may be handed to a
// runner, why the last attempt failed, and when the run's timeout ends it.
func (c *cmd) persistFlow(changed []string) error {
	for _, id := range changed {
		s := c.st.Steps[id]
		if _, err := c.tx.Exec(`UPDATE steps SET allowed = ?, not_before = ?, fail_cause = ? WHERE run_id = ? AND step_id = ?`,
			boolInt(s.Allowed), s.NotBefore, s.FailCause, c.st.ID, id); err != nil {
			return err
		}
	}
	if _, err := c.tx.Exec(`UPDATE runs SET deadline_at = ? WHERE id = ?`, c.st.deadline(), c.st.ID); err != nil {
		return err
	}
	for _, ev := range c.events {
		if ev.Type == RunCreated && c.st.Rerun != nil && len(c.st.Rerun.Froms) > 1 {
			text, _ := json.Marshal(c.st.Rerun.Froms)
			if _, err := c.tx.Exec(`UPDATE runs SET rerun_froms = ? WHERE id = ?`, string(text), c.st.ID); err != nil {
				return err
			}
		}
		if ev.Type == RunCreated && c.st.Trigger.PullRequestInto != "" {
			if _, err := c.tx.Exec(`UPDATE runs SET pull_request_into = ? WHERE id = ?`, c.st.Trigger.PullRequestInto, c.st.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// failureCause says why an attempt failed, from what its runner reported:
// refused (the runner's accept: section), a timeout, a failure of the
// machinery around the script (the workspace, the artifacts to fetch, an
// image that cannot be pulled, a secret store that does not answer), or
// else an exit. Anything not known counts as exit: that is the cause of a
// script that failed, and the one a retry without causes retries anyway.
func failureCause(res Result) string {
	switch {
	case res.Outcome == OutcomeRefused:
		return pipeline.CauseInfrastructure
	case res.ExitCode != nil:
		return pipeline.CauseExit
	case strings.HasPrefix(res.Error, "timed out after "):
		return pipeline.CauseTimeout
	}
	for _, prefix := range infrastructureErrors {
		if strings.HasPrefix(res.Error, prefix) {
			return pipeline.CauseInfrastructure
		}
	}
	if strings.HasPrefix(res.Error, "secret ") && strings.Contains(res.Error, "unavailable") {
		return pipeline.CauseInfrastructure
	}
	return pipeline.CauseExit
}

// infrastructureErrors are the beginnings of the errors runners report for
// failures that are not the step's own.
var infrastructureErrors = []string{
	"preparing the workspace",
	"preparing to fetch artifacts",
	"cannot pull ",
	"this runner has no image builders",
}

// retryDue says whether a failed attempt is retried, and after how long: the
// step must have retries left, and its retry's causes must include this
// one. A refusal is retried only when infrastructure is listed by name, since
// without that the same runner would refuse it again at once.
func (c *cmd) retryDue(def *pipeline.Step, s *StepState, cause string, refused bool) (retry bool, wait time.Duration) {
	if def == nil || c.st.CancelRequested || s.Failures+1 > def.Retry {
		return false, 0
	}
	if len(def.RetryOn) > 0 || !c.e.knows(schemaFlow) {
		if !def.RetriesCause(cause) {
			return false, 0
		}
	} else if refused {
		return false, 0
	}
	if !c.e.knows(schemaFlow) {
		return true, 0
	}
	return true, def.RetryDelay(s.Failures + 1)
}

// nextRetryDue is the earliest time a queued step that waits to be retried
// may be handed out, if any is later than now; 0 if none.
func (e *Engine) nextRetryDue(now int64) int64 {
	if !e.knows(schemaFlow) {
		return 0
	}
	var due sql.NullInt64
	if err := e.db.QueryRow(`SELECT MIN(not_before) FROM steps WHERE status = 'queued' AND not_before > ?`, now).Scan(&due); err != nil {
		return 0
	}
	return due.Int64
}

// ---- run timeouts ----

// expireRunTimeouts cancels runs open longer than their pipeline's timeout.
// Every server calls it, and the cancel is an event appended to the run's
// log, so exactly one wins: the others find the run already being canceled.
// A deployment the run holds is dealt with as for a cancel by hand
// (on_cancel is honoured).
func (e *Engine) expireRunTimeouts() {
	if !e.knows(schemaFlow) {
		return
	}
	rows, err := e.db.Query(`SELECT id FROM runs WHERE deadline_at > 0 AND deadline_at <= ? AND min_schema <= ?`, e.now().UnixMilli(), e.schema)
	if err != nil {
		e.log.Error("looking for runs past their timeout", "err", err)
		return
	}
	var due []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			e.log.Error("looking for runs past their timeout", "err", err)
			return
		}
		due = append(due, id)
	}
	rows.Close()
	for _, id := range due {
		err := e.withRun(id, func(c *cmd) error {
			if d := c.st.deadline(); d == 0 || d > c.now {
				return nil // over, canceled, paused or extended since the query above
			}
			var open []string
			if c.e.knows(schemaControl) {
				open = c.st.openDeployments()
			}
			c.emit(RunCancelRequested, Payload{Actor: "run timeout", Reason: RunTimeoutReason(c.st.Pipeline.Timeout)})
			c.advance()
			c.abortOnCancel(open, "run timeout")
			return nil
		})
		if err != nil && !errors.Is(err, ErrNeedsNewer) {
			e.log.Error("canceling a run past its timeout", "run", id, "err", err)
		}
	}
}

const runTimeoutReasonPrefix = "run timeout of "

// RunTimeoutReason is the cancel_reason of a run that reached its timeout.
func RunTimeoutReason(d pipeline.Duration) string {
	return runTimeoutReasonPrefix + d.Short() + " reached"
}

// ---- installation limits ----

// RunLimits are the installation's limits on pipelines (schema 24). They are
// configuration, so they live in the database: every server reads the same
// values, and an administrator changes them through the API, the CLI or
// the MCP server.
type RunLimits struct {
	// MaxRunTimeout is the longest timeout a pipeline may ask for, as a
	// duration such as 48h; empty means no limit beyond 720h.
	MaxRunTimeout string `json:"max_run_timeout"`
	UpdatedAt     int64  `json:"updated_at"`
	UpdatedBy     string `json:"updated_by,omitempty"`
}

// limitsName is the name of the one row of installation_limits.
const limitsName = "installation"

// RunLimits reads the installation's limits.
func (e *Engine) RunLimits() (RunLimits, error) {
	if !e.knows(schemaFlow) {
		return RunLimits{}, fmt.Errorf("%w: this server does not know installation limits (schema %d)", ErrConflict, schemaFlow)
	}
	return readRunLimits(e.db)
}

func readRunLimits(q interface {
	QueryRow(string, ...any) *sql.Row
}) (RunLimits, error) {
	var l RunLimits
	var max int64
	err := q.QueryRow(`SELECT max_run_timeout, updated_at, updated_by FROM installation_limits WHERE name = ?`, limitsName).Scan(&max, &l.UpdatedAt, &l.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if max > 0 {
		l.MaxRunTimeout = pipeline.Duration(time.Duration(max) * time.Millisecond).Short()
	}
	return l, nil
}

// SetRunLimits replaces the installation's limits and returns them before
// and after. A limit left empty is not set.
func (e *Engine) SetRunLimits(l RunLimits, actor string) (before, after RunLimits, err error) {
	if !e.knows(schemaFlow) {
		return before, after, fmt.Errorf("%w: this server does not know installation limits (schema %d)", ErrConflict, schemaFlow)
	}
	var max time.Duration
	if l.MaxRunTimeout != "" {
		d, perr := time.ParseDuration(l.MaxRunTimeout)
		switch {
		case perr != nil:
			return before, after, fmt.Errorf("%w: max_run_timeout %q is not a duration such as 48h", ErrInvalid, clipText(l.MaxRunTimeout, 40))
		case d < pipeline.MinRunTimeout || d > pipeline.MaxRunTimeout:
			return before, after, fmt.Errorf("%w: max_run_timeout must be between %s and %s", ErrInvalid,
				pipeline.Duration(pipeline.MinRunTimeout).Short(), pipeline.Duration(pipeline.MaxRunTimeout).Short())
		}
		max = d
	}
	unlock := e.writeLock()
	defer unlock()
	tx, err := e.db.Begin()
	if err != nil {
		return before, after, err
	}
	defer tx.Rollback()
	if before, err = readRunLimits(tx); err != nil {
		return before, after, err
	}
	if _, err = tx.Exec(`INSERT INTO installation_limits(name, max_run_timeout, updated_at, updated_by) VALUES(?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET max_run_timeout = excluded.max_run_timeout, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		limitsName, max.Milliseconds(), e.now().UnixMilli(), actor); err != nil {
		return before, after, err
	}
	if after, err = readRunLimits(tx); err != nil {
		return before, after, err
	}
	return before, after, tx.Commit()
}

// CheckRunLimits checks a pipeline against the installation's limits: its
// timeout may not be longer than max_run_timeout. The problem names the
// limit.
func (e *Engine) CheckRunLimits(p *pipeline.Pipeline) pipeline.Problems {
	if !e.knows(schemaFlow) || p == nil || p.Timeout <= 0 {
		return nil
	}
	l, err := readRunLimits(e.db)
	if err != nil {
		e.log.Error("reading the installation's limits", "err", err)
		return nil
	}
	if l.MaxRunTimeout == "" {
		return nil
	}
	max, err := time.ParseDuration(l.MaxRunTimeout)
	if err != nil || p.Timeout.D() <= max {
		return nil
	}
	return pipeline.Problems{{Line: p.TimeoutLine, Path: "timeout", Message: fmt.Sprintf(
		"the run timeout %s is longer than max_run_timeout %s, the most this installation allows (an administrator sets it with launchway run-limits)",
		p.Timeout.Short(), l.MaxRunTimeout)}}
}

// emitFailure records a failed attempt with its cause. A failure that is
// retried is not final, so it is not an allowed one.
func (c *cmd) emitFailure(p *Payload, res Result, retrying bool) {
	if c.e.knows(schemaFlow) {
		p.Cause = failureCause(res)
	}
	p.retrying = retrying
	c.emit(StepFinished, *p)
}

// queueRetry queues a step for another attempt after a failure, with the
// pause its retry asks for, if any.
func (c *cmd) queueRetry(stepID string, s *StepState, wait time.Duration) {
	q := Payload{Step: stepID, Attempt: s.Attempt + 1, Reason: "retry"}
	if wait > 0 {
		q.NotBefore, q.Requires = c.now+wait.Milliseconds(), schemaFlow
		q.Reason = "retry in " + pipeline.Duration(wait).Short()
	}
	c.emit(StepQueued, q)
}

// rerunFroms reads the steps a re-run started from, as runs.rerun_froms
// keeps them; nil for a re-run from one step (or none).
func rerunFroms(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(text), &out) != nil {
		return nil
	}
	return out
}
