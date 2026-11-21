package engine

import (
	"fmt"
	"slices"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/policy"
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
	// StepWaitingS: no runner holds the step, and it waits: an approval for
	// a person, a deploy step for its environment's lock or for what deploy
	// runners observe (StepState.WaitKind says which).
	StepWaitingS StepStatus = "waiting"
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
	// PullRequestSecrets says whether the steps of a pull-request run may
	// read secrets. It means nothing for other runs.
	PullRequestSecrets bool
	// SelfApproval is whether the starter could approve their own run when
	// it was created; nil for a run created before it was recorded.
	SelfApproval *bool
	// CancelReason is why the run was asked to cancel, when it was not by
	// hand, and SupersededBy the newer run that superseded it, if that is
	// why. Both are cleared by a resume.
	CancelReason string
	SupersededBy *Superseder
	// Lane is the lane the run joined, and how it stands in it (schema 24);
	// nil if it joined none.
	Lane *LaneState
	// Excluded is how long the run has spent in ways its timeout does not
	// count (schema 24): waiting for approvals that were decided, and the
	// time between its end and a resume (milliseconds).
	Excluded int64
	// Commands are the commands given to the run's deployments' rollouts,
	// in the order given (schema 14; see control.go).
	Commands []*CommandState
	// RunGate is the latest policy decision on the run as a whole, made
	// when it was created, re-run or last resumed; nil if none (schema 16).
	// A blocked one fails each step as it would start.
	RunGate *policy.Evaluation
	// Policy lists every policy decision on the run, in order.
	Policy []*policy.Evaluation

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

// LaneState is a run's place in a lane.
type LaneState struct {
	Name, Key, Label string
	// State is holding (the run goes, or went, with the lane), waiting,
	// replaced (a newer run took its place or the lane) or skipped.
	State string
	// Other is the run on the other side: the holder a waiting or skipped run
	// is behind, or the run that replaced this one; OtherNumber its number.
	Other       string
	OtherNumber int
	Since       int64 // when the state began
}

// The states of a run in a lane.
const (
	LaneHolding  = "holding"
	LaneWaiting  = "waiting"
	LaneReplaced = "replaced"
	LaneSkipped  = "skipped"
)

// laneWaiting reports whether the run waits for its lane, so that none of its
// steps are queued.
func (st *RunState) laneWaiting() bool { return st.Lane != nil && st.Lane.State == LaneWaiting }

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
	// Allowed says a failed step has on_failure: continue (schema 24): it
	// failed for good, and the run goes on as if it had succeeded.
	Allowed bool
	// NotBefore is, for a step queued to be retried after a wait, the time
	// before which it is not handed to a runner (schema 24); FailCause is
	// why the last failed attempt failed: exit, timeout or infrastructure.
	NotBefore int64
	FailCause string
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
	// Deploy and rollback steps only. WaitKind is what a waiting step waits
	// for (WaitLock or WaitObserve; empty for an approval), and Dep is the
	// step's deployment, once it waits for the lock or has one.
	WaitKind string
	Dep      *DeployState
	// Gate is the latest policy decision on this step (schema 16): before
	// a deploy or rollout step acted, or an image step was queued.
	Gate *policy.Evaluation
	// Evidence is what the step's latest attempt reported, for policies:
	// its test totals, findings, SBOM and image, as in the event that
	// ended it.
	Evidence *StepEvidence
	// Ver, for a verify step, is its verification once started (schema
	// 19); AI, for an ai step, what its last call used.
	Ver *VerifyState
	AI  *AIRecord
}

// StepEvidence is what a step attempt reported, from the event that ended
// it.
type StepEvidence struct {
	Tests    *TestSummary
	Findings *FindingsSummary
	SBOM     *SBOMSummary
	Image    *ImageRecord
	Caches   []CacheResult // what the attempt's build caches did (schema 25)
}

// What a waiting deploy or rollback step waits for.
const (
	WaitLock    = "lock"    // the environment's lock, held by another deployment
	WaitObserve = "observe" // deploy runners to observe that it is done
)

// DeployState is a deploy or rollback step's deployment, as its events
// describe it. A rollback step's has the ID of the deployment it rolls back.
type DeployState struct {
	ID          string
	Environment string
	Service     string
	Target      string
	Application string
	Mode        string
	Version     string
	Previous    *DeployRef
	Holder      *DeployRef // waiting for the lock: who holds it
	OpenedAt    int64
	// Action is the action queued, running or last done (apply or revert),
	// ActionDone whether it is done, and Facts what the runner did.
	Action     string
	ActionDone bool
	Facts      *DeployFacts
	Commit     string // observing: the commit Argo CD must have synced
	Phase      string
	Detail     string
	Phases     int // deploy.phase events in this attempt
	Closed     bool
	Verdict    string
	// Protected is whether the environment was protected when the
	// deployment opened; nil for one that predates the record.
	Protected *bool
	// Command, for a rollout step, is the command it gave in this attempt
	// (schema 14).
	Command string
}

// Opened reports whether the deployment was opened (it took the lock).
func (d *DeployState) Opened() bool { return d != nil && d.ID != "" }

// selfApproval says whether the person who started the run may approve it:
// what was in force when it was created, or, for a run that predates the
// record, the server's setting now.
func (st *RunState) selfApproval(e *Engine) bool {
	if st.SelfApproval != nil {
		return *st.SelfApproval
	}
	return e.live().selfOK
}

// secretsAllowed reports whether the run's steps may read secrets: not in a
// draft run or a pull-request run unless the project allowed it when the run
// was created (for a draft, or when it was last resumed).
func (st *RunState) secretsAllowed() bool {
	switch {
	case st.Draft != nil:
		return st.Draft.Secrets
	case st.Trigger.Event == pipeline.EventPullRequest:
		return st.PullRequestSecrets
	}
	return true
}

// canceledBecause is the reason given to steps canceled because the run
// was asked to cancel.
func (st *RunState) canceledBecause() string {
	if st.CancelReason != "" {
		return "the run was canceled: " + st.CancelReason
	}
	return "the run was canceled"
}

// stoppedBecause is the reason given to a running step its runner stopped
// because the run was asked to cancel.
func (st *RunState) stoppedBecause() string {
	if st.CancelReason != "" {
		return "canceled: " + st.CancelReason
	}
	return "canceled"
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
	knownStepKinds = []string{"", pipeline.KindStep, pipeline.KindApproval, pipeline.KindDeploy, pipeline.KindRollback, pipeline.KindRolloutStep,
		pipeline.KindImage, pipeline.KindVerify, pipeline.KindAI}
	knownOutcomes  = []string{string(StepSucceeded), string(StepFailed)}
	knownRunEnds   = []string{string(RunSucceeded), string(RunFailed), string(RunCanceled)}
	knownSkipKinds = []string{"condition", "upstream", SkipNotNeeded, SkipSuperseded}
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
		return st.checkCreated(p)
	}
	if st.Steps == nil {
		st.notUnderstood("a %s event before the run was created", ev.Type)
		return false
	}
	if p.Step != "" && st.Steps[p.Step] == nil {
		st.notUnderstood("step %s, which is not in the pipeline", p.Step)
		return false
	}
	if p.Step == "" && ev.Type != RunCancelRequested && ev.Type != RunResumed && ev.Type != RunFinished && ev.Type != PolicyEvaluated && !ev.Type.isLane() {
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
		(p.Outcome == string(StepFailed) && p.Allowed) ||
		(p.Outcome == string(StepSkippedS) && slices.Contains(knownSkipKinds, p.SkipKind)))):
		st.notUnderstood("a reused step with outcome %q", p.Outcome)
		return false
	}
	return true
}

// checkCreated reads a run.created payload: whether the pipeline is there,
// its step kinds are known and the schemas its keys need are understood.
func (st *RunState) checkCreated(p Payload) bool {
	if p.Pipeline == nil {
		st.notUnderstood("a run without a pipeline")
		return false
	}
	for _, s := range p.Pipeline.Steps {
		if !slices.Contains(knownStepKinds, s.Kind) {
			st.notUnderstood("step kind %q", s.Kind)
		}
	}
	// What the pipeline's keys need, in the order the schemas came. A release
	// older than a schema does not know its keys, so it reads the pipeline as
	// not understood (see decodePayload); a build limited to an older schema
	// for testing says the same here. A step that, for example, collects test
	// reports or holds a deploy lock would be silently skipped by a server that
	// does not know it, so such a run is left alone.
	for _, k := range []struct {
		uses   bool
		schema int
		what   string
	}{
		// Pull-request runs and the pipeline keys of schema 9.
		{p.Pipeline.UsesSchema9() || (p.Trigger != nil && p.Trigger.Event == pipeline.EventPullRequest), schemaPullRequests,
			"a pipeline key of schema %d (on.pull_request or supersede), or a pull-request run"},
		{p.Pipeline.UsesSchema10(), schemaTests, "a pipeline key of schema %d (reports)"},
		{p.Pipeline.UsesSchema11(), schemaDeploy, "deploy or rollback steps (schema %d)"},
		{p.Pipeline.UsesSchema12(), schemaArtifacts, "a pipeline key of schema %d (artifacts, fetch, reports.findings or reports.sbom)"},
		{usesSchema13(p), schemaTriggers, "a pipeline key or trigger of schema %d (inputs, for_each, a schedule or a webhook)"},
		{p.Pipeline.UsesSchema15(), schemaImages, "image steps or deploy changes that pin a digest (schema %d)"},
		{p.Pipeline.UsesSchema18(), schemaReports, "test attachments, coverage or HTML reports (schema %d)"},
		{p.Pipeline.UsesSchemaVerify(), schemaVerify, "verify or ai steps (schema %d)"},
		{p.Pipeline.UsesSchemaFlow(), schemaFlow, "a pipeline key of schema %d (flow control: when.output, on_failure, retry waits, timeout, lane or into)"},
	} {
		if !k.uses {
			continue
		}
		st.Schema = max(st.Schema, k.schema)
		if !st.understands(k.schema) {
			st.notUnderstood(k.what, k.schema)
		}
	}
	// Rollout steps, and the keys that control a rollout.
	if p.Pipeline.UsesSchemaControl() || p.Pipeline.Requires > 0 {
		st.Schema = max(st.Schema, schemaControl, p.Pipeline.Requires)
		if need := max(schemaControl, p.Pipeline.Requires); !st.understands(need) {
			what := "rollout steps or keys that control a rollout"
			if p.Pipeline.Requires >= schemaPools {
				what = "a pipeline key of a newer schema, such as runner:"
			}
			st.notUnderstood("%s (schema %d)", what, need)
		}
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
		st.applyCreated(ev)

	case RunCancelRequested:
		st.CancelRequested = true
		st.CancelReason = p.Reason
		st.SupersededBy = nil
		if p.SupersededBy != nil {
			by := *p.SupersededBy
			st.SupersededBy = &by
		}

	case RunResumed:
		st.applyResumed(ev, p)

	case RunFinished:
		st.Status = RunStatus(p.Status)
		st.FinishedAt = ev.At

	case StepQueued:
		st.applyQueued(ev, s)

	case StepSkipped:
		s.Status = StepSkippedS
		s.WaitKind = ""
		s.Error = p.Reason
		s.SkipKind = p.SkipKind
		s.FinishedAt = ev.At

	case StepLeased:
		st.applyLeased(ev, s)

	case StepStarted:
		s.Status = StepRunning
		s.StartedAt = ev.At

	case StepFinished:
		s.applyFinished(ev, p)

	case StepLost:
		s.Lost++
		s.Error = p.Reason
		s.LeaseID = ""

	case StepCanceled:
		st.endApprovalWait(s, ev.At)
		s.Status = StepCanceledS
		s.WaitKind = ""
		s.Error = p.Reason
		s.FinishedAt = ev.At
		s.LeaseID = ""
		if e := evidenceOf(p); e != nil {
			s.Evidence = e
		}

	case StepReset:
		st.applyReset(ev, s)

	case StepWaiting:
		st.applyWaiting(ev, s)

	case StepApproved, StepRejected:
		st.applyDecided(ev, s)

	case StepReused:
		s.applyReused(p, ev)

	case RunLaneWaiting, RunLaneStarted, RunLaneReplaced, RunLaneSkipped:
		st.applyLane(ev)

	case PolicyEvaluated:
		if p.Policy == nil {
			break
		}
		st.Policy = append(st.Policy, p.Policy)
		if s == nil {
			st.RunGate = p.Policy
		} else {
			s.Gate = p.Policy
		}

	case VerifyStarted, VerifyInterval, VerifyVerdict, VerifyOverride:
		st.applyVerify(ev, s)

	default:
		st.applyDeploy(ev, s)
	}
}

// applyCreated folds run.created: what the run is, and what was in force when
// it was created.
func (st *RunState) applyCreated(ev Event) {
	p := ev.Data
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
	st.PullRequestSecrets = p.PullRequestSecrets != nil && *p.PullRequestSecrets
	st.SelfApproval = p.SelfApproval
	st.Status = RunQueued
	st.CreatedAt = ev.At
	st.Steps = map[string]*StepState{}
	st.Order = nil
	for i, step := range p.Pipeline.Steps {
		st.Steps[step.ID] = &StepState{ID: step.ID, Position: i, Status: StepPending}
		st.Order = append(st.Order, step.ID)
	}
}

// applyQueued folds StepQueued.
func (st *RunState) applyQueued(ev Event, s *StepState) {
	p := ev.Data
	if p.Attempt != s.Attempt && s.Dep != nil {
		s.Dep.Phases = 0
	}
	s.Status = StepQueuedS
	s.WaitKind = ""
	if d := p.Deploy; d != nil {
		st.queueAction(s, d)
	}
	s.Attempt = p.Attempt
	s.QueuedAt = ev.At
	s.StartedAt, s.FinishedAt = 0, 0
	s.ExitCode, s.Error, s.Runner, s.LeaseID = nil, "", "", ""
	s.Outputs = nil
	s.Excerpt = ""
	s.Evidence = nil
	s.Allowed = false
	s.NotBefore = p.NotBefore
}

// applyLeased folds StepLeased.
func (st *RunState) applyLeased(ev Event, s *StepState) {
	p := ev.Data
	s.Status = StepLeasedS
	s.NotBefore = 0
	s.LeaseID = p.Lease
	s.Runner = p.Runner
	// A deploy runner's actions need no workspace: they do not move the
	// run to that runner. Nor do verify and ai steps.
	if def := st.Pipeline.Step(p.Step); def != nil && def.IsDeployKind() {
		return
	}
	if def := st.Pipeline.Step(p.Step); def != nil && (def.IsVerify() || def.IsAI()) {
		if st.Status == RunQueued {
			st.Status, st.StartedAt = RunRunning, ev.At
		}
		return
	}
	if p.Runner != st.Runner {
		st.Runner, st.Workspace = p.Runner, p.Lease
	}
	if st.Status == RunQueued {
		st.Status = RunRunning
		st.StartedAt = ev.At
	}
}

// applyReset folds StepReset.
func (st *RunState) applyReset(ev Event, s *StepState) {
	s.Status = StepPending
	s.Failures, s.Lost = 0, 0
	s.Error, s.SkipKind, s.Excerpt = "", "", ""
	s.Allowed = false
	s.NotBefore, s.FailCause = 0, ""
	s.ExitCode = nil
	s.StartedAt, s.FinishedAt = 0, 0
	s.clearDecision()
	s.WaitKind, s.Dep = "", nil
	s.Gate, s.Evidence = nil, nil
	s.Ver, s.AI = nil, nil
}

// applyWaiting folds StepWaiting.
func (st *RunState) applyWaiting(ev Event, s *StepState) {
	p := ev.Data
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
}

// applyDecided folds StepApproved and StepRejected.
func (st *RunState) applyDecided(ev Event, s *StepState) {
	p := ev.Data
	st.endApprovalWait(s, ev.At)
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

// endApprovalWait counts the time an approval waited, now that it ends,
// among what the run's timeout does not count.
func (st *RunState) endApprovalWait(s *StepState, at int64) {
	if s.Status == StepWaitingS && s.StartedAt > 0 && at > s.StartedAt && st.Pipeline != nil {
		if def := st.Pipeline.Step(s.ID); def != nil && def.IsApproval() {
			st.Excluded += at - s.StartedAt
		}
	}
}

// approvalWaiting reports whether an approval is waiting for a decision
// now, which stops the run's timeout.
func (st *RunState) approvalWaiting() bool {
	for _, id := range st.Order {
		if s := st.Steps[id]; s.Status == StepWaitingS {
			if def := st.Pipeline.Step(id); def != nil && def.IsApproval() {
				return true
			}
		}
	}
	return false
}

// deadline is when the run's timeout ends it (Unix ms), or 0 if it has none
// that can run out now: the pipeline sets no timeout, the run is over or
// being canceled, or it waits for an approval (time spent waiting for a
// person does not count against the timeout; the deadline is worked out
// again when they decide).
func (st *RunState) deadline() int64 {
	if st.Pipeline == nil || st.Pipeline.Timeout <= 0 || st.Status.Terminal() || st.CancelRequested || st.approvalWaiting() || st.laneWaiting() {
		return 0
	}
	return st.CreatedAt + st.Pipeline.Timeout.D().Milliseconds() + st.Excluded
}

// evidenceOf is what an event that ends a step attempt says it reported,
// or nil.
func evidenceOf(p Payload) *StepEvidence {
	if p.Tests == nil && p.Findings == nil && p.SBOM == nil && p.Image == nil && len(p.Caches) == 0 {
		return nil
	}
	return &StepEvidence{Tests: p.Tests, Findings: p.Findings, SBOM: p.SBOM, Image: p.Image, Caches: p.Caches}
}
