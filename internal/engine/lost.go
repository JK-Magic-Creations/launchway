package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// lose records that a step's runner disappeared, and queues it again unless
// it has been lost too often, its retry.on leaves out infrastructure, or the
// run is being canceled. A lost attempt is an infrastructure failure, so it
// follows the step's retry policy for those: retry.on says whether it is
// tried again, and retry.wait and retry.backoff say after how long. Lost
// attempts are bounded by the server's maxLost, not by retry.times, which
// counts the step's own failures.
func (c *cmd) lose(stepID, reason string) {
	s := c.st.Steps[stepID]
	c.emit(StepLost, Payload{Step: stepID, Attempt: s.Attempt, Reason: reason})
	def := c.st.Pipeline.Step(stepID)
	policy := c.e.knows(schemaFlow) && def != nil
	switch {
	case c.st.CancelRequested:
		c.emit(StepCanceled, Payload{Step: stepID, Attempt: s.Attempt, Reason: c.st.canceledBecause()})
	case s.Lost <= c.e.maxLost && !(policy && !def.RetriesCause(pipeline.CauseInfrastructure)):
		q := Payload{Step: stepID, Attempt: s.Attempt + 1, Reason: "requeued after: " + reason}
		if policy {
			if wait := def.RetryDelay(s.Lost); wait > 0 {
				q.NotBefore, q.Requires = c.now+wait.Milliseconds(), schemaFlow
				q.Reason = "requeued in " + pipeline.Duration(wait).Short() + " after: " + reason
			}
		}
		c.emit(StepQueued, q)
	case s.Lost <= c.e.maxLost:
		c.emit(StepFinished, Payload{Step: stepID, Attempt: s.Attempt, Outcome: string(StepFailed), Cause: pipeline.CauseInfrastructure,
			Reason: reason + " (retry.on does not include infrastructure)"})
	default:
		c.emit(StepFinished, Payload{Step: stepID, Attempt: s.Attempt, Outcome: string(StepFailed), Cause: infraIf(c.e.knows(schemaFlow)),
			Reason: fmt.Sprintf("%s (gave up after %d tries)", reason, s.Lost)})
	}
}

// infraIf is the infrastructure cause where schema 24 can record one.
func infraIf(knows bool) string {
	if knows {
		return pipeline.CauseInfrastructure
	}
	return ""
}
