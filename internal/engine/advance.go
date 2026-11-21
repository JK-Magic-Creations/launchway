package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// needsState says whether every step def needs is done (ready), and, if one
// of them did not succeed, which (blocked). A failure its step tolerates
// counts as success.
func (c *cmd) needsState(def *pipeline.Step) (ready bool, blocked string) {
	ready = true
	for _, n := range def.Needs {
		switch dep := c.st.Steps[n]; dep.Status {
		case StepSucceeded:
		case StepFailed:
			if !dep.Allowed && blocked == "" {
				blocked = fmt.Sprintf("%s %s", n, dep.Status)
			}
		case StepSkippedS, StepCanceledS:
			if blocked == "" {
				blocked = fmt.Sprintf("%s %s", n, dep.Status)
			}
		default:
			ready = false
		}
	}
	return ready, blocked
}

// whenSkip says why a step's when condition keeps it from running, or
// returns "" if it holds. A missing output is told apart from a mismatch.
func (c *cmd) whenSkip(def *pipeline.Step, ctx pipeline.Context) string {
	ctx = c.condContext(ctx)
	if why := def.When.OutputWhy(ctx); why != "" {
		return why
	}
	if !def.When.Match(ctx) {
		return "its when condition did not match"
	}
	return ""
}

// finishWhenDone closes the deployments whose steps are done and, when every
// step has ended, finishes the run: failed if a step failed that its pipeline
// does not tolerate, else canceled if one was, else succeeded. A step still
// pending or running leaves the run open.
func (c *cmd) finishWhenDone() {
	st := c.st
	c.closeDeployments()
	failed, canceled := false, false
	for _, id := range st.Order {
		switch st.Steps[id].Status {
		case StepSucceeded, StepSkippedS:
		case StepFailed:
			// A failure its step tolerates does not fail the run.
			failed = failed || !st.Steps[id].Allowed
		case StepCanceledS:
			canceled = true
		default:
			return // something is still pending or running
		}
	}
	status := RunSucceeded
	switch {
	case failed:
		status = RunFailed
	case canceled:
		status = RunCanceled
	}
	c.emit(RunFinished, Payload{Status: string(status)})
}
