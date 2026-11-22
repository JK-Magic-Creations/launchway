package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/policy"
)

// EventType names something that happened to a run. Events are never changed
// or deleted; a run's state is whatever its events add up to.
type EventType string

const (
	RunCreated         EventType = "run.created"
	RunCancelRequested EventType = "run.cancel_requested"
	RunResumed         EventType = "run.resumed"
	RunFinished        EventType = "run.finished"
	// RunReviewed is an approver acknowledging an emergency rollback that
	// needed review (schema 32): payload actor and comment.
	RunReviewed EventType = "run.reviewed"

	StepQueued   EventType = "step.queued"   // ready, waiting for a runner
	StepSkipped  EventType = "step.skipped"  // will not run
	StepLeased   EventType = "step.leased"   // a runner has taken it
	StepStarted  EventType = "step.started"  // the runner began executing it
	StepFinished EventType = "step.finished" // succeeded or failed
	StepLost     EventType = "step.lost"     // the runner stopped responding
	StepCanceled EventType = "step.canceled"
	StepReset    EventType = "step.reset" // made pending again by a resume

	StepWaiting  EventType = "step.waiting"  // an approval is waiting for a person to decide
	StepApproved EventType = "step.approved" // a person approved it; it succeeds
	StepRejected EventType = "step.rejected" // a person rejected it, or nobody decided in time; it fails
	// StepApprovalRecorded is one approval of several an approval step asks
	// for (approvers.count above 1, schema 27); the step goes on waiting
	// until the last one, which is a step.approved.
	StepApprovalRecorded EventType = "step.approval_recorded"

	// StepReused records, in a re-run, a step that is not run again: its
	// outcome and outputs are those it had in an earlier run.
	StepReused EventType = "step.reused"

	// Deployments (schema 11; see deployments.go). All are step events.
	DeployWaitingLock EventType = "deploy.waiting_lock" // a deploy step waits for its environment's lock
	DeployOpened      EventType = "deploy.opened"       // a deploy step took the lock and opened a deployment
	DeployActionDone  EventType = "deploy.action_done"  // a deploy runner finished an action (commit and sync, or revert)
	DeployObserving   EventType = "deploy.observing"    // the step waits for what deploy runners observe
	DeployPhase       EventType = "deploy.phase"        // what is observed moved to another phase
	DeployClosed      EventType = "deploy.closed"       // every step of the deployment is done; the lock is released

	// Commands on a deployment's rollout (schema 14; see control.go). Step
	// events, of the step that gave the command (the deploy step, for a
	// person's).
	DeployCommand          EventType = "deploy.command"           // promote, promote-full, abort or retry asked for, with what it expects
	DeployCommandWithdrawn EventType = "deploy.command_withdrawn" // withdrawn before a deploy runner took it
	DeployCommandTaken     EventType = "deploy.command_taken"     // a deploy runner took it, until a time
	DeployCommandDone      EventType = "deploy.command_done"      // applied, already applied, not applied, or failed

	// PolicyEvaluated records a policy decision (schema 16; see policy.go):
	// at the run's start (no step), before a deploy or rollout step acts,
	// or before an image step that pushes is handed to a runner. It holds
	// the policies, the facts and the waivers it was judged from.
	PolicyEvaluated EventType = "policy.evaluated"

	// Queues (schema 37; see queues.go). Run events, with no step.
	RunHeld     EventType = "run.held"     // a limit, or an operator, holds the run back: none of its steps is queued
	RunAdmitted EventType = "run.admitted" // the run may go: its steps are queued
	RunBumped   EventType = "run.bumped"   // an operator moved the run to the front of the queue
	// RunTrusted is a maintainer approving a restricted run (schema 37): actor and comment.
	RunTrusted EventType = "run.trusted"

	// Lanes (schema 24; see lanes.go). Run events, with no step.
	RunLaneWaiting  EventType = "run.lane_waiting"  // the lane is held by another run: this one waits its turn
	RunLaneStarted  EventType = "run.lane_started"  // this run took the lane and goes
	RunLaneReplaced EventType = "run.lane_replaced" // a newer run took this run's place in the lane (waiting run) or the lane (holder)
	RunLaneSkipped  EventType = "run.lane_skipped"  // the lane was busy, and the lane says to skip: this run is canceled at once

	// Verifications (schema 19; see verify.go). Step events of the verify
	// step.
	VerifyStarted  EventType = "verify.started"  // the window, the resolved parameters and the baseline it is judged against
	VerifyInterval EventType = "verify.interval" // what a deploy runner read in one interval
	VerifyVerdict  EventType = "verify.verdict"  // the decision, with a model's explanation if one was asked
	VerifyOverride EventType = "verify.override" // a person marked it passed or failed
)

// eventSince is the schema version that brought each event type. A server
// understands the events of the schemas it knows; a run with any other
// event is left alone (see RunState.NotUnderstood). A new event type comes
// with a migration, even one with no SQL, so that its schema names it.
var eventSince = map[EventType]int{
	RunCreated: 1, RunCancelRequested: 1, RunResumed: 1, RunFinished: 1,
	StepQueued: 1, StepSkipped: 1, StepLeased: 1, StepStarted: 1, StepFinished: 1,
	StepLost: 1, StepCanceled: 1, StepReset: 1,
	StepWaiting: 4, StepApproved: 4, StepRejected: 4,
	StepReused:           8,
	StepApprovalRecorded: schemaParity2,
	DeployWaitingLock:    11, DeployOpened: 11, DeployActionDone: 11, DeployObserving: 11, DeployPhase: 11, DeployClosed: 11,
	DeployCommand: 14, DeployCommandWithdrawn: 14, DeployCommandTaken: 14, DeployCommandDone: 14,
	PolicyEvaluated: 16,
	VerifyStarted:   19, VerifyInterval: 19, VerifyVerdict: 19, VerifyOverride: 19,
	RunReviewed: schemaRollback,
	RunHeld:     schemaQueues, RunAdmitted: schemaQueues, RunBumped: schemaQueues, RunTrusted: schemaQueues,
	RunLaneWaiting: schemaFlow, RunLaneStarted: schemaFlow, RunLaneReplaced: schemaFlow, RunLaneSkipped: schemaFlow,
}

// Event is one entry in the log.
type Event struct {
	Seq   int64     `json:"seq"`
	RunID string    `json:"run_id"`
	Type  EventType `json:"type"`
	At    int64     `json:"at"` // Unix milliseconds
	Data  Payload   `json:"data"`

	// problem, set when the event is read, is what in it this server does
	// not understand, such as a pipeline key a newer server wrote.
	problem string
}

// Payload carries the details of an event. Each type uses a few fields.
type Payload struct {
	// run.created
	Project  string             `json:"project,omitempty"`
	Number   int                `json:"number,omitempty"`
	Pipeline *pipeline.Pipeline `json:"pipeline,omitempty"`
	Trigger  *Trigger           `json:"trigger,omitempty"`
	Source   *Source            `json:"source,omitempty"`
	Draft    *Draft             `json:"draft,omitempty"` // a draft run: the text that runs, which is not in Git

	// step events
	Step     string            `json:"step,omitempty"`
	Attempt  int               `json:"attempt,omitempty"`
	Lease    string            `json:"lease,omitempty"`
	Runner   string            `json:"runner,omitempty"`
	Outcome  string            `json:"outcome,omitempty"` // succeeded or failed
	ExitCode *int              `json:"exit_code,omitempty"`
	Outputs  map[string]string `json:"outputs,omitempty"`
	SkipKind string            `json:"skip_kind,omitempty"` // condition or upstream
	Deadline int64             `json:"deadline,omitempty"`  // step.waiting: when nobody deciding rejects it (Unix ms)
	Comment  string            `json:"comment,omitempty"`   // step.approved and step.rejected
	Excerpt  string            `json:"excerpt,omitempty"`   // step.finished, failed: the last lines of output, masked
	// Why is, on step.finished of a failed deploy, rollout or rollback
	// step, the structured cause with its evidence (see StepCause).
	Why *StepCause `json:"why,omitempty"`
	// Allowed, on a step.finished that failed for good, or a step.reused of
	// one, says that the step has on_failure: continue: the failure does not
	// fail the run and the steps that need it run as if it had succeeded
	// (schema 24).
	Allowed bool `json:"allowed,omitempty"`
	// Cause, on a step.finished that failed (schema 24), is why: exit,
	// timeout or infrastructure (see failureCause). NotBefore, on the
	// step.queued of a retry that waits, is the time before which no runner
	// is given the step (Unix ms).
	Cause     string `json:"cause,omitempty"`
	NotBefore int64  `json:"not_before,omitempty"`
	// WaitCode, on a step.queued of a step that waits for room (schema 37),
	// is the stable code of the reason (see capacity_wait.go) and Reason the
	// sentence; on the step.finished that gives up waiting it is the code of
	// what the step was waiting for.
	WaitCode string `json:"wait_code,omitempty"`

	// run.lane_* (schema 24): the lane, and the run on the other side of it.
	Lane *LaneEvent `json:"lane,omitempty"`
	// run.created, run.held, run.admitted (schema 37): the run's kind for
	// limits and order, and for a held run the stable code and the sentence.
	Queue *QueueEvent `json:"queue,omitempty"`
	// run.created (schema 37): the run is restricted, and why.
	Restricted *RestrictedEvent `json:"restricted,omitempty"`

	// shared
	Reason string `json:"reason,omitempty"`
	Actor  string `json:"actor,omitempty"`
	Status string `json:"status,omitempty"` // run.finished

	// run.resumed of a draft run: whether its steps may read secrets from
	// now on, as the configuration says at the time of the resume.
	DraftSecrets *bool `json:"draft_secrets,omitempty"`

	// retrying, set by the code that emits a step.finished it will follow
	// with a retry, says the failure is not final, so it is not allowed.
	retrying bool

	// Requires, on any event, is the oldest schema whose servers understand
	// it. Servers ignore fields they do not know, so a newer server that adds
	// a field older servers must not ignore sets this; a server that knows
	// less leaves the run alone. Empty means the event type's own schema.
	Requires int `json:"requires,omitempty"`

	// step.leased: what the attempt was given (schema 8). Older servers
	// ignore it, which is harmless: it only records.
	Inputs *StepInputs `json:"inputs,omitempty"`
	// run.created of a re-run: the run it re-runs, and from which step.
	Rerun *Rerun `json:"rerun,omitempty"`
	// step.reused: where the outcome and outputs come from. Outcome is
	// succeeded or skipped; Actor and Comment are an approval's decision.
	Reused *Reused `json:"reused,omitempty"`

	// step.waiting of an approval that asks for several approvals (schema
	// 27): how many distinct people must approve, and where the number comes
	// from (the step's approvers, or its environment's min_approvals).
	// step.reused: the people whose approvals the reused decision had.
	Needed     int              `json:"needed,omitempty"`
	NeededFrom string           `json:"needed_from,omitempty"`
	Approvals  []ApprovalRecord `json:"approvals,omitempty"`

	// run.created of a pull-request run (schema 9): whether its steps may
	// read secrets, as the project's configuration said when it was
	// created. Older servers would hand secrets out regardless; they leave
	// such runs alone because the pipeline of a pull-request run always has
	// on.pull_request, a key they do not know (see RunState.check).
	PullRequestSecrets *bool `json:"pull_request_secrets,omitempty"`
	// run.created: whether people may approve their own runs, as the
	// server's setting said when the run was created. The run keeps it, so
	// changing the setting never changes what a run in flight allows. Runs
	// created before this was recorded use the setting in effect.
	SelfApproval *bool `json:"self_approval,omitempty"`
	// run.cancel_requested (schema 9): the newer run that superseded this
	// one, if that is why it is cancelled; Reason says so in words. Older
	// servers ignore it, which is harmless: it only records.
	SupersededBy *Superseder `json:"superseded_by,omitempty"`
	// step.finished and step.canceled (schema 10): what the attempt's test
	// reports said, in brief; the individual results are in test_cases.
	// Older servers ignore it, which is harmless: it only records.
	Tests *TestSummary `json:"tests,omitempty"`
	// step.finished and step.canceled (schema 12): what the attempt's SARIF
	// files and SBOM said, in brief, and the artifacts it kept; the details
	// are in findings, sbom_components and artifacts. Older servers ignore
	// them, which is harmless: they only record.
	Findings  *FindingsSummary  `json:"findings,omitempty"`
	SBOM      *SBOMSummary      `json:"sbom,omitempty"`
	Artifacts []ArtifactSummary `json:"artifacts,omitempty"`
	// step.finished and step.canceled (schema 25): what each of the step's
	// caches did. Older servers ignore it, which is harmless: it only
	// records.
	Caches []CacheResult `json:"caches,omitempty"`
	// step.finished and step.canceled of an image step (schema 15): what it
	// built, pushed and signed. Older servers never read it: runs with image
	// steps have min_schema 15.
	Image *ImageRecord `json:"image,omitempty"`
	// step.finished and step.canceled (schema 18): what the attempt's
	// coverage reports said, in brief; each file's counts are in
	// coverage_files. Older servers never read it: runs whose pipeline
	// declares coverage have min_schema 18.
	Coverage *CoverageSummary `json:"coverage,omitempty"`
	// deploy.* events, and step.queued of a deploy or rollback step (schema
	// 11): the deployment and what happened to it. Never a credential.
	Deploy *DeployEvent `json:"deploy,omitempty"`
	// policy.evaluated (schema 16): the decision and what it was judged
	// from.
	Policy *policy.Evaluation `json:"policy,omitempty"`
	// verify.* events (schema 19): the verification and what happened to
	// it.
	Verify *VerifyEvent `json:"verify,omitempty"`
	// step.finished and step.canceled of an ai step (schema 19): which model
	// answered and what it cost. Older servers never read it: runs with ai
	// steps have min_schema 19.
	AI *AIRecord `json:"ai,omitempty"`
}

// QueueEvent carries what the queue events say about a run: its Class (push_protected,
// push, pull_request, schedule or manual), whether automation started it, its
// Priority (higher goes first), and, on run.held, the Code and Reason, and
// Manual for a hold an operator put on.
type QueueEvent struct {
	Class      string `json:"class,omitempty"`
	Automation bool   `json:"automation,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	Code       string `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Manual     bool   `json:"manual,omitempty"`
}

// LaneEvent carries the details of the run.lane_* events: the lane a run is
// in, and the run on the other side: the holder a waiting or skipped run is
// behind, or the run that replaced this one.
type LaneEvent struct {
	Name   string `json:"name"`
	Key    string `json:"key"`   // what identifies the lane in the project
	Label  string `json:"label"` // the lane in words: release/ref main
	Run    string `json:"run,omitempty"`
	Number int    `json:"number,omitempty"`
}

// DeployEvent carries the details of the deploy.* events. Each type uses a
// few fields.
type DeployEvent struct {
	// deploy.opened, and step.queued of a deploy or rollback step: the
	// deployment's ID (dep_...). step.queued also says which Action.
	ID          string `json:"id,omitempty"`
	Environment string `json:"environment,omitempty"` // waiting_lock, opened
	Service     string `json:"service,omitempty"`     // waiting_lock, opened
	// deploy.waiting_lock: the deployment that holds the lock.
	Holder *DeployRef `json:"holder,omitempty"`
	// deploy.opened
	Target string `json:"target,omitempty"`
	// Protected, on deploy.opened: whether the environment was protected
	// when the deployment opened. The deployment keeps it, so editing the
	// environment never unprotects a deployment in flight.
	Protected   *bool      `json:"protected,omitempty"`
	Application string     `json:"application,omitempty"`
	Mode        string     `json:"mode,omitempty"` // write_back
	Version     string     `json:"version,omitempty"`
	Previous    *DeployRef `json:"previous,omitempty"` // what the environment ran before, if Launchway knows
	// deploy.opened and deploy.observing: when the whole step fails if it is
	// not done (Unix ms).
	Deadline int64 `json:"deadline,omitempty"`
	// deploy.action_done, and step.queued: apply (commit and sync) or
	// revert (revert and sync).
	Action  string       `json:"action,omitempty"`
	Outcome string       `json:"outcome,omitempty"` // action_done: succeeded or failed
	Facts   *DeployFacts `json:"facts,omitempty"`
	// deploy.observing: what the step waits for (healthy), and the commit
	// that must be synced.
	Until  string `json:"until,omitempty"`
	Commit string `json:"commit,omitempty"`
	// deploy.phase
	Phase  string `json:"phase,omitempty"`
	Detail string `json:"detail,omitempty"`
	// deploy.closed: how the deployment ended (see Verdicts).
	Verdict string `json:"verdict,omitempty"`
	// deploy.command*: the command (schema 14).
	Command *CommandEvent `json:"command,omitempty"`
}

// DeployRef names a deployment.
type DeployRef struct {
	Deployment string `json:"deployment"`
	Run        string `json:"run"`
	Number     int    `json:"number"`
	Step       string `json:"step,omitempty"`
	Version    string `json:"version,omitempty"`
}

// DeployFacts are what a deploy runner did in an action.
type DeployFacts struct {
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Commit carries the change: made now, found from an earlier try, or,
	// when Git already held the version (Changed false), the branch's tip.
	Commit   string   `json:"commit,omitempty"`
	Changed  bool     `json:"changed"`
	Existing bool     `json:"existing,omitempty"`
	Files    []string `json:"files,omitempty"`
	// Revert, for a revert: the revert commit; Reverted false means there
	// was nothing to revert.
	Revert   string `json:"revert,omitempty"`
	Reverted bool   `json:"reverted,omitempty"`
	// Sync says what was asked of Argo CD: requested, not needed, skipped
	// (request: never), or why not.
	Sync string `json:"sync,omitempty"`
	// Abort, for a revert with abort_first, says what became of aborting
	// the rollout first (schema 14; older servers ignore it, as it only
	// records).
	Abort string `json:"abort,omitempty"`
}

// Superseder is the run that superseded another.
type Superseder struct {
	Run    string `json:"run"`
	Number int    `json:"number"`
}

// Rerun links a re-run to the run it re-runs.
type Rerun struct {
	Of       string `json:"of"`        // the run's ID
	OfNumber int    `json:"of_number"` // and number
	From     string `json:"from"`      // the step the re-run starts from (the first, if several)
	// Froms are all the steps the re-run starts from, when it starts from
	// several (schema 24). Older servers read From only, so such a run
	// carries Requires and they leave it alone.
	Froms []string `json:"froms,omitempty"`
}

// Reused says where a reused step actually ran: the earlier run, and the
// attempt there. When the step was itself reused in the run being re-run,
// this is where it ran, not that run.
type Reused struct {
	Run       string `json:"run"`
	Number    int    `json:"number"`
	Attempt   int    `json:"attempt"`
	DecidedAt int64  `json:"decided_at,omitempty"` // approvals: when it was decided
}

// Draft is the pipeline text a draft run runs. It is not in Git, so it is
// kept with the run, together with a hash that names it in the audit trail.
type Draft struct {
	Text string `json:"text"`
	Hash string `json:"hash"` // sha256: and the hex SHA-256 of Text
	// Secrets says whether the run's steps may read secrets. When false, a
	// step that declares secrets fails before it is handed to a runner.
	Secrets bool `json:"secrets"`
}

// DraftHash names pipeline text: sha256: and its hex SHA-256.
func DraftHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Trigger records why a run started.
type Trigger struct {
	Event  string `json:"event"`            // push, manual, draft or pull_request
	Ref    string `json:"ref,omitempty"`    // refs/heads/main, or refs/pull/12/head
	Branch string `json:"branch,omitempty"` // main
	Tag    string `json:"tag,omitempty"`
	SHA    string `json:"sha,omitempty"`
	// CommitTime is the commit's committer time, UTC, RFC 3339 with a Z and
	// whole seconds. It is read when the run is created and kept in the run's
	// creation event, so a re-run or a resume sees the same value.
	CommitTime string `json:"commit_time,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Message    string `json:"message,omitempty"` // commit subject
	Author     string `json:"author,omitempty"`
	// PullRequest is the pull request's number, for event pull_request.
	PullRequest int `json:"pull_request,omitempty"`
	// The pull request as its host described it (an outside Git host only):
	// the title, who opened it, whether it is a draft, and where the host
	// shows it. Fork, below, is set from the same answer.
	PullRequestTitle  string `json:"pull_request_title,omitempty"`
	PullRequestAuthor string `json:"pull_request_author,omitempty"`
	PullRequestURL    string `json:"pull_request_url,omitempty"`
	PullRequestDraft  bool   `json:"pull_request_draft,omitempty"`
	// Fork says the pull request is from a repository other than the
	// project's own; its build caches are isolated (see cacheScopes). The
	// forge integration sets it.
	Fork bool `json:"fork,omitempty"`
	// PullRequestInto is the branch the pull request targets, as the
	// project's forge said when the run was created (schema 24); empty when
	// the pipeline does not filter by target and none was read.
	PullRequestInto string `json:"pull_request_into,omitempty"`
	// Inputs are the run's input values, canonical, by name: every input
	// the pipeline declares (schema 13).
	Inputs map[string]string `json:"inputs,omitempty"`
	// Schedule, for event schedule, is the schedule's cron expression and
	// time zone, and ScheduledFor the time it fired for (Unix ms).
	Schedule     string `json:"schedule,omitempty"`
	ScheduledFor int64  `json:"scheduled_for,omitempty"`
	// Webhook, for event webhook, is the webhook trigger's name.
	Webhook string `json:"webhook,omitempty"`
	// Changed is the files the push or pull request changed, when they could
	// be told; nil, or Known false, when not. Steps with when: { changed }
	// and the triggers' path filters judge by it.
	Changed *pipeline.ChangeSet `json:"changed,omitempty"`
}

// CommitTimeAt formats a Unix time in the form of Trigger.CommitTime. Zero
// (no time known) gives "".
func CommitTimeAt(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format(time.RFC3339)
}

// SourceDateEpoch is the commit time as Unix seconds, for reproducible
// builds, or "" when the run has none.
func (t Trigger) SourceDateEpoch() string {
	ts, err := time.Parse(time.RFC3339, t.CommitTime)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(ts.Unix(), 10)
}

// Context returns what step conditions are tested against.
func (t Trigger) Context() pipeline.Context {
	return pipeline.Context{Event: t.Event, Branch: t.Branch, Tag: t.Tag, Inputs: t.Inputs, Changed: t.Changed}
}

// Source says where a runner gets the files a run works on.
type Source struct {
	Kind string `json:"kind"`           // git or dir
	URL  string `json:"url,omitempty"`  // git: what to clone
	Path string `json:"path,omitempty"` // dir: a local directory used as-is
	// Hosted and Credential are set by the server when it leases the job, for a
	// repository hosted by this same installation: Hosted is the repository's
	// path (owner/name) and Credential a short-lived read-only credential
	// bound to that repository and this run. A runner then clones from the
	// server it already talks to instead of from URL, with no setup of its own.
	// Never stored with the run.
	Hosted     string `json:"hosted,omitempty"`
	Credential string `json:"credential,omitempty"`
	// Outside is set at lease time for a Flight whose repository is kept on
	// another Git host through a connection: the credential to send to that
	// host's address only. Never stored with the run, never written to the
	// workspace's git configuration.
	Outside *OutsideAuth `json:"outside,omitempty"`
	// Libraries are the shared libraries the run's templates came from, at
	// the commits they were expanded from. The runner puts each one's files
	// in the workspace, at .launchway/lib/<name>.
	Libraries []LibrarySource `json:"libraries,omitempty"`
}

// OutsideAuth is a run's credential for a repository on another Git host.
type OutsideAuth struct {
	// URL is the clone address; the credential goes to its origin only.
	URL      string `json:"url"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
	// Resolve pins git to the addresses the server's egress check saw, as
	// http.curloptResolve entries (HOST:PORT:ADDRESS[,ADDRESS]).
	Resolve []string `json:"resolve,omitempty"`
}

// LibrarySource is where a runner gets one library's files.
type LibrarySource struct {
	Name   string `json:"name"`   // as the pipeline calls it
	URL    string `json:"url"`    // what to fetch
	Commit string `json:"commit"` // the commit the run uses, always
	// Hosted and Credential: as Source's, set at lease time for a library hosted by this installation.
	Hosted     string `json:"hosted,omitempty"`
	Credential string `json:"credential,omitempty"`
}
