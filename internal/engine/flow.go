package engine

import (
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

// persistFlow writes what schema 24 keeps in the steps table: whether a
// failed step was allowed to fail.
func (c *cmd) persistFlow(changed []string) error {
	for _, id := range changed {
		if _, err := c.tx.Exec(`UPDATE steps SET allowed = ? WHERE run_id = ? AND step_id = ?`,
			boolInt(c.st.Steps[id].Allowed), c.st.ID, id); err != nil {
			return err
		}
	}
	return nil
}
