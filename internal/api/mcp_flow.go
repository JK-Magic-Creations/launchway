package api

import (
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// stepLists are the steps of a run that failed (for good, not allowed),
// run, wait and are queued, as describeRun finds them.
type stepLists struct{ failed, running, waiting, queued []string }

// note says in words why a step is in its state, and files it under the
// list it belongs to.
func (l *stepLists) note(s *engine.StepView, sb *stepBrief, text *stepText) {
	switch s.Status {
	case engine.StepFailed:
		sb.Why = fmt.Sprintf("failed on attempt %d", s.Attempt)
		if s.ExitCode != nil {
			sb.Why += fmt.Sprintf(" with exit code %d", *s.ExitCode)
		}
		if s.AllowedFailure {
			// Not a failure of the run: its step has on_failure: continue.
			sb.AllowedFailure = true
			sb.Why += " (allowed: on_failure: continue, so the run goes on)"
		} else {
			l.failed = append(l.failed, s.ID)
		}
		text.Error, text.Output = untrusted(s.Error, 500), untrusted(s.Excerpt, maxExcerptBytes)
	case engine.StepWaitingS:
		l.waiting = append(l.waiting, s.ID)
		switch {
		case s.Kind == pipeline.KindApproval:
			sb.Why = "waiting for a person to approve or reject it"
			if s.Deadline > 0 {
				sb.Why += "; rejected by itself at " + ts(s.Deadline) + " if nobody decides"
			}
			text.Message = untrusted(s.Message, 500)
		case s.WaitKind == engine.WaitLock:
			sb.Why = "waiting for the lock on environment " + s.Environment + ", which another deployment holds"
		case s.WaitKind == engine.WaitObserve:
			sb.Why = "deployed; waiting for deploy runners to observe that it is healthy"
		default:
			sb.Why = "waiting"
		}
	case engine.StepQueuedS:
		l.queued = append(l.queued, s.ID)
		sb.Why = "waiting for a runner to take it"
		if s.NotBefore > time.Now().UnixMilli() {
			sb.NotBefore = ts(s.NotBefore)
			sb.Why = fmt.Sprintf("waiting to be retried (attempt %d of %d): a runner is given it from %s", s.Attempt, s.Retry+1, sb.NotBefore)
		}
	case engine.StepLeasedS, engine.StepRunning:
		l.running = append(l.running, s.ID)
		if s.Runner != "" {
			sb.Why = "running on " + s.Runner
		}
	case engine.StepSkippedS:
		switch s.SkipKind {
		case "condition":
			sb.Why = "skipped: its when condition did not hold"
		case "upstream":
			sb.Why = "skipped: a step it needs did not succeed"
		case engine.SkipNotNeeded:
			sb.Why = "skipped: not needed"
		case engine.SkipSuperseded:
			sb.Why = "skipped: a newer run of the same service took its place"
		default:
			sb.Why = "skipped"
		}
		if s.Error != "" {
			text.Error = untrusted(s.Error, 500)
		}
	case engine.StepCanceledS:
		if s.Error != "" {
			text.Error = untrusted(s.Error, 500)
		}
	}
}
