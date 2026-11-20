package engine

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// EventType names something that happened to a run. Events are never changed
// or deleted; a run's state is whatever its events add up to.
type EventType string

const (
	RunCreated         EventType = "run.created"
	RunCancelRequested EventType = "run.cancel_requested"
	RunResumed         EventType = "run.resumed"
	RunFinished        EventType = "run.finished"

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

	// Verifications (schema 20; see verify.go). Step events of the verify
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
	StepReused:        8,
	DeployWaitingLock: 11, DeployOpened: 11, DeployActionDone: 11, DeployObserving: 11, DeployPhase: 11, DeployClosed: 11,
	DeployCommand: 14, DeployCommandWithdrawn: 14, DeployCommandTaken: 14, DeployCommandDone: 14,
	VerifyStarted: 20, VerifyInterval: 20, VerifyVerdict: 20, VerifyOverride: 20,
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

	// shared
	Reason string `json:"reason,omitempty"`
	Actor  string `json:"actor,omitempty"`
	Status string `json:"status,omitempty"` // run.finished

	// run.resumed of a draft run: whether its steps may read secrets from
	// now on, as the configuration says at the time of the resume.
	DraftSecrets *bool `json:"draft_secrets,omitempty"`

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

	// run.created of a pull-request run (schema 9): whether its steps may
	// read secrets, as the project's configuration said when it was
	// created. Older servers would hand secrets out regardless; they leave
	// such runs alone because the pipeline of a pull-request run always has
	// on.pull_request, a key they do not know (see RunState.check).
	PullRequestSecrets *bool `json:"pull_request_secrets,omitempty"`
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
	// step.finished and step.canceled of an image step (schema 15): what it
	// built, pushed and signed. Older servers never read it: runs with image
	// steps have min_schema 15.
	Image *ImageRecord `json:"image,omitempty"`
	// deploy.* events, and step.queued of a deploy or rollback step (schema
	// 11): the deployment and what happened to it. Never a credential.
	Deploy *DeployEvent `json:"deploy,omitempty"`
	// verify.* events (schema 20): the verification and what happened to
	// it.
	Verify *VerifyEvent `json:"verify,omitempty"`
	// step.finished and step.canceled of an ai step (schema 20): which model
	// answered and what it cost. Older servers never read it: runs with ai
	// steps have min_schema 20.
	AI *AIRecord `json:"ai,omitempty"`
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
	Target      string     `json:"target,omitempty"`
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
	From     string `json:"from"`      // the step the re-run starts from
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
	Event   string `json:"event"`            // push, manual, draft or pull_request
	Ref     string `json:"ref,omitempty"`    // refs/heads/main, or refs/pull/12/head
	Branch  string `json:"branch,omitempty"` // main
	Tag     string `json:"tag,omitempty"`
	SHA     string `json:"sha,omitempty"`
	Actor   string `json:"actor,omitempty"`
	Message string `json:"message,omitempty"` // commit subject
	Author  string `json:"author,omitempty"`
	// PullRequest is the pull request's number, for event pull_request.
	PullRequest int `json:"pull_request,omitempty"`
	// Inputs are the run's input values, canonical, by name: every input
	// the pipeline declares (schema 13).
	Inputs map[string]string `json:"inputs,omitempty"`
	// Schedule, for event schedule, is the schedule's cron expression and
	// time zone, and ScheduledFor the time it fired for (Unix ms).
	Schedule     string `json:"schedule,omitempty"`
	ScheduledFor int64  `json:"scheduled_for,omitempty"`
	// Webhook, for event webhook, is the webhook trigger's name.
	Webhook string `json:"webhook,omitempty"`
}

// Context returns what step conditions are tested against.
func (t Trigger) Context() pipeline.Context {
	return pipeline.Context{Event: t.Event, Branch: t.Branch, Tag: t.Tag, Inputs: t.Inputs}
}

// Source says where a runner gets the files a run works on.
type Source struct {
	Kind string `json:"kind"`           // git or dir
	URL  string `json:"url,omitempty"`  // git: what to clone
	Path string `json:"path,omitempty"` // dir: a local directory used as-is
	// Libraries are the shared libraries the run's templates came from, at
	// the commits they were expanded from. The runner puts each one's files
	// in the workspace, at .launchway/lib/<name>.
	Libraries []LibrarySource `json:"libraries,omitempty"`
}

// LibrarySource is where a runner gets one library's files.
type LibrarySource struct {
	Name   string `json:"name"`   // as the pipeline calls it
	URL    string `json:"url"`    // what to fetch
	Commit string `json:"commit"` // the commit the run uses, always
}
