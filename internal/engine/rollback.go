package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// advanceRollback decides a rollback step once every step it needs is done:
// it runs if and only if its deployment was opened and one of those steps
// did not succeed (failed, or was skipped or canceled because something
// failed). A failure its step tolerates (on_failure: continue) is not one:
// it counts as success here as it does for the steps that need it. It
// reports whether it changed the step.
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
		case StepFailed:
			// A failure its step tolerates counts as success, as for any
			// step that needs it (see advance).
			if !dep.Allowed && bad == "" {
				bad = fmt.Sprintf("%s %s", n, dep.Status)
			}
		case StepCanceledS:
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
