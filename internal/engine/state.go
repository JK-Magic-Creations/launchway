package engine

import (
	"fmt"
	"slices"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

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
	Draft           *Draft // nil unless this is a draft run
	Status          RunStatus
	CancelRequested bool
	CreatedAt       int64
	StartedAt       int64
	FinishedAt      int64
	Steps           map[string]*StepState
	Order           []string
	LastSeq         int64
	// Runner holds the run's workspace: it took the run's latest step. See
	// Engine.Lease.
	Runner string
	// Workspace names the workspace Runner holds: the lease that began its
	// hold on the run. It changes whenever the run moves to another runner,
	// even back to one that held it before.
	Workspace string
	// Rerun, for a re-run, is the run it re-runs and the step it started from.
	Rerun *Rerun

	// Schema is the oldest schema whose servers understand every event of
	// the run (runs.min_schema).
	Schema int
	// NotUnderstood lists what in the run's events this server does not
	// understand: event types, pipeline keys, step kinds or outcomes that a
	// newer server wrote. Acting on such a run could corrupt it, so the
	// engine refuses to change it (ErrNeedsNewer) and leaves it to a newer
	// server; other runs are not affected.
	NotUnderstood []string
	// vocab is the newest schema whose events are understood; 0 means
	// every schema this build has.
	vocab int
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
	Excerpt    string // a failed attempt: the last lines of its output, masked by the runner
	// Approvals only.
	Deadline  int64  // when a waiting approval is rejected because nobody decided
	DecidedBy string // who approved or rejected it; empty if it expired
	DecidedAt int64
	Comment   string
	// A step reused by a re-run: the run (and its number) where it actually
	// ran, and the attempt there. Empty for a step that ran in this run.
	ReusedFrom    string
	ReusedNumber  int
	ReusedAttempt int
}

// clearDecision forgets an approval's last wait and decision.
func (s *StepState) clearDecision() {
	s.Deadline, s.DecidedAt = 0, 0
	s.DecidedBy, s.Comment = "", ""
}

// Fold rebuilds a run's state from its events, in order, understanding
// every event this build knows.
func Fold(events []Event) *RunState { return fold(events, 0) }

// fold is Fold understanding only the events of schemas up to vocab (0:
// all), as a server that knows only those would.
func fold(events []Event, vocab int) *RunState {
	var st *RunState
	for _, ev := range events {
		if st == nil {
			st = &RunState{vocab: vocab}
		}
		st.apply(ev)
	}
	return st
}

// Values a server of this version understands, inside events.
var (
	knownStepKinds = []string{"", pipeline.KindStep, pipeline.KindApproval}
	knownOutcomes  = []string{string(StepSucceeded), string(StepFailed)}
	knownRunEnds   = []string{string(RunSucceeded), string(RunFailed), string(RunCanceled)}
	knownSkipKinds = []string{"condition", "upstream"}
)

// understands reports whether this state's server knows events of schema v.
func (st *RunState) understands(v int) bool {
	return st.vocab == 0 || v <= st.vocab
}

// notUnderstood records something in the run this server does not know.
func (st *RunState) notUnderstood(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if !slices.Contains(st.NotUnderstood, msg) {
		st.NotUnderstood = append(st.NotUnderstood, msg)
	}
}

// check reports whether an event can be applied, recording why not if it
// cannot. Once a run has something not understood its state is shown but
// never acted on, so skipping the event is safe.
func (st *RunState) check(ev Event) bool {
	p := ev.Data
	since, ok := eventSince[ev.Type]
	switch {
	case !ok || !st.understands(since):
		st.notUnderstood("event type %s", ev.Type)
		return false
	case p.Requires > 0 && !st.understands(p.Requires):
		st.notUnderstood("a %s event that needs schema %d", ev.Type, p.Requires)
		return false
	}
	if ev.problem != "" {
		// Read leniently, so it can still be shown; never acted on.
		st.notUnderstood("%s", ev.problem)
	}
	st.Schema = max(st.Schema, since, p.Requires)
	if ev.Type == RunCreated {
		if p.Pipeline == nil {
			st.notUnderstood("a run without a pipeline")
			return false
		}
		for _, s := range p.Pipeline.Steps {
			if !slices.Contains(knownStepKinds, s.Kind) {
				st.notUnderstood("step kind %q", s.Kind)
			}
		}
		return true
	}
	if st.Steps == nil {
		st.notUnderstood("a %s event before the run was created", ev.Type)
		return false
	}
	if p.Step != "" && st.Steps[p.Step] == nil {
		st.notUnderstood("step %s, which is not in the pipeline", p.Step)
		return false
	}
	if p.Step == "" && ev.Type != RunCancelRequested && ev.Type != RunResumed && ev.Type != RunFinished {
		st.notUnderstood("a %s event without a step", ev.Type)
		return false
	}
	switch {
	case ev.Type == RunFinished && !slices.Contains(knownRunEnds, p.Status):
		st.notUnderstood("run status %q", p.Status)
		return false
	case ev.Type == StepFinished && !slices.Contains(knownOutcomes, p.Outcome):
		st.notUnderstood("step outcome %q", p.Outcome)
		return false
	case ev.Type == StepSkipped && !slices.Contains(knownSkipKinds, p.SkipKind):
		st.notUnderstood("skip kind %q", p.SkipKind)
		return false
	case ev.Type == StepReused && (p.Reused == nil || !(p.Outcome == string(StepSucceeded) ||
		(p.Outcome == string(StepSkippedS) && slices.Contains(knownSkipKinds, p.SkipKind)))):
		st.notUnderstood("a reused step with outcome %q", p.Outcome)
		return false
	}
	return true
}

// apply moves the state forward by one event. It must stay deterministic:
// the same events always produce the same state.
func (st *RunState) apply(ev Event) {
	p := ev.Data
	st.LastSeq = ev.Seq
	if !st.check(ev) {
		return
	}
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
		if p.Draft != nil {
			d := *p.Draft
			st.Draft = &d
		}
		if p.Rerun != nil {
			r := *p.Rerun
			st.Rerun = &r
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
		if st.Draft != nil && p.DraftSecrets != nil {
			d := *st.Draft
			d.Secrets = *p.DraftSecrets
			st.Draft = &d
		}
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
		s.Excerpt = ""

	case StepSkipped:
		s.Status = StepSkippedS
		s.Error = p.Reason
		s.SkipKind = p.SkipKind
		s.FinishedAt = ev.At

	case StepLeased:
		s.Status = StepLeasedS
		s.LeaseID = p.Lease
		s.Runner = p.Runner
		if p.Runner != st.Runner {
			st.Runner, st.Workspace = p.Runner, p.Lease
		}
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
		s.Excerpt = p.Excerpt
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
		s.Error, s.SkipKind, s.Excerpt = "", "", ""
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

	case StepReused:
		if p.Outcome == string(StepSucceeded) {
			s.Status = StepSucceeded
		} else {
			s.Status = StepSkippedS
			s.SkipKind, s.Error = p.SkipKind, p.Reason
		}
		s.Outputs = p.Outputs
		s.FinishedAt = ev.At
		s.ReusedFrom, s.ReusedNumber, s.ReusedAttempt = p.Reused.Run, p.Reused.Number, p.Reused.Attempt
		s.DecidedBy, s.DecidedAt, s.Comment = p.Actor, p.Reused.DecidedAt, p.Comment
	}
}
