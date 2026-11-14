package engine

import "github.com/jk-magic-creations/launchway/internal/pipeline"

// RunStatus is the state of a whole run.
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
)

// Terminal reports whether the run has finished.
func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunCanceled
}

// StepStatus is the state of one step.
type StepStatus string

const (
	StepPending   StepStatus = "pending" // waiting for the steps it needs
	StepQueuedS   StepStatus = "queued"
	StepLeasedS   StepStatus = "leased"
	StepRunning   StepStatus = "running"
	StepSucceeded StepStatus = "succeeded"
	StepFailed    StepStatus = "failed"
	StepSkippedS  StepStatus = "skipped"
	StepCanceledS StepStatus = "canceled"
	StepWaitingS  StepStatus = "waiting" // an approval waiting for a person; no runner holds it
)

// Terminal reports whether the step will not change again without a resume.
func (s StepStatus) Terminal() bool {
	return s == StepSucceeded || s == StepFailed || s == StepSkippedS || s == StepCanceledS
}

// Active reports whether a runner holds the step.
func (s StepStatus) Active() bool { return s == StepLeasedS || s == StepRunning }

// RunState is a run as its events describe it.
type RunState struct {
	ID              string
	Project         string
	Number          int
	Pipeline        *pipeline.Pipeline
	Trigger         Trigger
	Source          Source
	Status          RunStatus
	CancelRequested bool
	CreatedAt       int64
	StartedAt       int64
	FinishedAt      int64
	Steps           map[string]*StepState
	Order           []string
	LastSeq         int64
}

// StepState is one step as its events describe it.
type StepState struct {
	ID         string
	Position   int
	Status     StepStatus
	Attempt    int // the attempt now queued, running or last finished
	Failures   int // failures since the run was created or last resumed
	Lost       int // times a runner stopped responding, likewise
	QueuedAt   int64
	StartedAt  int64
	FinishedAt int64
	ExitCode   *int
	Error      string
	SkipKind   string
	Runner     string
	LeaseID    string
	Outputs    map[string]string
	// Approvals only.
	Deadline  int64  // when a waiting approval is rejected because nobody decided
	DecidedBy string // who approved or rejected it; empty if it expired
	DecidedAt int64
	Comment   string
}

// clearDecision forgets an approval's last wait and decision.
func (s *StepState) clearDecision() {
	s.Deadline, s.DecidedAt = 0, 0
	s.DecidedBy, s.Comment = "", ""
}

// Fold rebuilds a run's state from its events, in order.
func Fold(events []Event) *RunState {
	var st *RunState
	for _, ev := range events {
		if st == nil {
			st = &RunState{}
		}
		st.apply(ev)
	}
	return st
}

// apply moves the state forward by one event. It must stay deterministic:
// the same events always produce the same state.
func (st *RunState) apply(ev Event) {
	p := ev.Data
	st.LastSeq = ev.Seq
	var s *StepState
	if p.Step != "" {
		s = st.Steps[p.Step]
	}
	switch ev.Type {
	case RunCreated:
		st.ID = ev.RunID
		st.Project = p.Project
		st.Number = p.Number
		st.Pipeline = p.Pipeline
		if p.Trigger != nil {
			st.Trigger = *p.Trigger
		}
		if p.Source != nil {
			st.Source = *p.Source
		}
		st.Status = RunQueued
		st.CreatedAt = ev.At
		st.Steps = map[string]*StepState{}
		st.Order = nil
		for i, step := range p.Pipeline.Steps {
			st.Steps[step.ID] = &StepState{ID: step.ID, Position: i, Status: StepPending}
			st.Order = append(st.Order, step.ID)
		}

	case RunCancelRequested:
		st.CancelRequested = true

	case RunResumed:
		st.CancelRequested = false
		st.Status = RunRunning
		st.FinishedAt = 0

	case RunFinished:
		st.Status = RunStatus(p.Status)
		st.FinishedAt = ev.At

	case StepQueued:
		s.Status = StepQueuedS
		s.Attempt = p.Attempt
		s.QueuedAt = ev.At
		s.StartedAt, s.FinishedAt = 0, 0
		s.ExitCode, s.Error, s.Runner, s.LeaseID = nil, "", "", ""
		s.Outputs = nil

	case StepSkipped:
		s.Status = StepSkippedS
		s.Error = p.Reason
		s.SkipKind = p.SkipKind
		s.FinishedAt = ev.At

	case StepLeased:
		s.Status = StepLeasedS
		s.LeaseID = p.Lease
		s.Runner = p.Runner
		if st.Status == RunQueued {
			st.Status = RunRunning
			st.StartedAt = ev.At
		}

	case StepStarted:
		s.Status = StepRunning
		s.StartedAt = ev.At

	case StepFinished:
		if p.Outcome == string(StepSucceeded) {
			s.Status = StepSucceeded
		} else {
			s.Status = StepFailed
			s.Failures++
		}
		s.ExitCode = p.ExitCode
		s.Error = p.Reason
		s.Outputs = p.Outputs
		s.FinishedAt = ev.At
		s.LeaseID = ""

	case StepLost:
		s.Lost++
		s.Error = p.Reason
		s.LeaseID = ""

	case StepCanceled:
		s.Status = StepCanceledS
		s.Error = p.Reason
		s.FinishedAt = ev.At
		s.LeaseID = ""

	case StepReset:
		s.Status = StepPending
		s.Failures, s.Lost = 0, 0
		s.Error, s.SkipKind = "", ""
		s.ExitCode = nil
		s.StartedAt, s.FinishedAt = 0, 0
		s.clearDecision()

	case StepWaiting:
		s.Status = StepWaitingS
		s.Attempt = p.Attempt
		// It starts waiting now; how long it waited is shown like a step's run time.
		s.QueuedAt, s.StartedAt, s.FinishedAt = ev.At, ev.At, 0
		s.Error = ""
		s.clearDecision()
		s.Deadline = p.Deadline
		if st.Status == RunQueued {
			st.Status = RunRunning
			st.StartedAt = ev.At
		}

	case StepApproved, StepRejected:
		if ev.Type == StepApproved {
			s.Status = StepSucceeded
			s.Error = ""
		} else {
			s.Status = StepFailed
			s.Failures++
			s.Error = p.Reason
		}
		s.FinishedAt = ev.At
		s.DecidedBy, s.DecidedAt, s.Comment = p.Actor, ev.At, p.Comment
	}
}
