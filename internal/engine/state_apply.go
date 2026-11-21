package engine

import "strings"

// These are the larger cases of RunState.apply, kept apart so that it stays
// readable.

// applyResumed applies run.resumed: the run is open again, and, as its
// timeout is concerned, either starts afresh (it was canceled by that very
// timeout) or does not count the time it was over.
func (st *RunState) applyResumed(ev Event, p Payload) {
	if st.Draft != nil && p.DraftSecrets != nil {
		d := *st.Draft
		d.Secrets = *p.DraftSecrets
		st.Draft = &d
	}
	timedOut := strings.HasPrefix(st.CancelReason, runTimeoutReasonPrefix)
	st.CancelRequested = false
	st.CancelReason, st.SupersededBy = "", nil
	st.RunGate = nil // a resume is judged again
	st.Status = RunRunning
	switch {
	case timedOut && st.Pipeline != nil && ev.At > st.CreatedAt:
		// A run its own timeout canceled has used its whole budget, so a
		// resume starts the clock again from now (the ceiling the
		// installation sets still applies: see checkRunLimits).
		st.Excluded = ev.At - st.CreatedAt
	case st.FinishedAt > 0 && ev.At > st.FinishedAt:
		st.Excluded += ev.At - st.FinishedAt // the run's timeout does not run while it is over
	}
	st.FinishedAt = 0
}

// applyFinished applies step.finished: the attempt ended, and for a failure
// whether its pipeline tolerates it and why it failed.
func (s *StepState) applyFinished(ev Event, p Payload) {
	s.WaitKind = ""
	if p.Outcome == string(StepSucceeded) {
		s.Status = StepSucceeded
	} else {
		s.Status = StepFailed
		s.Failures++
	}
	s.ExitCode = p.ExitCode
	s.Error = p.Reason
	s.Excerpt = p.Excerpt
	s.Why = p.Why
	s.Outputs = p.Outputs
	s.FinishedAt = ev.At
	s.LeaseID = ""
	s.Allowed = s.Status == StepFailed && p.Allowed
	s.FailCause = ""
	if s.Status == StepFailed {
		s.FailCause = p.Cause
	}
	s.NotBefore = 0
	s.Evidence = evidenceOf(p)
	if p.AI != nil {
		s.AI = p.AI
	}
}

// applyReused applies step.reused: a re-run took over an earlier run's
// outcome for the step.
func (s *StepState) applyReused(p Payload, ev Event) {
	switch p.Outcome {
	case string(StepSucceeded):
		s.Status = StepSucceeded
	case string(StepFailed):
		s.Status, s.Allowed = StepFailed, true
		s.Error = p.Reason
	default:
		s.Status = StepSkippedS
		s.SkipKind, s.Error = p.SkipKind, p.Reason
	}
	s.Outputs = p.Outputs
	s.FinishedAt = ev.At
	s.ReusedFrom, s.ReusedNumber, s.ReusedAttempt = p.Reused.Run, p.Reused.Number, p.Reused.Attempt
	s.DecidedBy, s.DecidedAt, s.Comment = p.Actor, p.Reused.DecidedAt, p.Comment
	s.Approvals = p.Approvals
}
