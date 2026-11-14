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
)

// Event is one entry in the log.
type Event struct {
	Seq   int64     `json:"seq"`
	RunID string    `json:"run_id"`
	Type  EventType `json:"type"`
	At    int64     `json:"at"` // Unix milliseconds
	Data  Payload   `json:"data"`
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
	Event   string `json:"event"`            // push, manual or draft
	Ref     string `json:"ref,omitempty"`    // refs/heads/main
	Branch  string `json:"branch,omitempty"` // main
	Tag     string `json:"tag,omitempty"`
	SHA     string `json:"sha,omitempty"`
	Actor   string `json:"actor,omitempty"`
	Message string `json:"message,omitempty"` // commit subject
	Author  string `json:"author,omitempty"`
}

// Context returns what step conditions are tested against.
func (t Trigger) Context() pipeline.Context {
	return pipeline.Context{Event: t.Event, Branch: t.Branch, Tag: t.Tag}
}

// Source says where a runner gets the files a run works on.
type Source struct {
	Kind string `json:"kind"`           // git or dir
	URL  string `json:"url,omitempty"`  // git: what to clone
	Path string `json:"path,omitempty"` // dir: a local directory used as-is
}
