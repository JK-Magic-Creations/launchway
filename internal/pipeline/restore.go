package pipeline

// Restore makes a rollback step the step of a rollback run: a run a person
// asked for, after a deployment succeeded, to put an environment's service
// back to an earlier version. Such a step names no deploy step of its own
// pipeline (Deployment is empty); it carries everything it needs, recorded
// when the run was asked for, because the deployments it undoes were made
// by other runs. The step's Environment, Service, ArgoCD and Rollout fields
// say where and how to watch the result, copied from the deployment step
// that made the version being restored.
//
// It is never written in a pipeline file: the server builds the run's
// pipeline (see engine.RequestRollback).
type Restore struct {
	// To is the earlier deployment whose version is restored.
	To RestoreTo `json:"to"`
	// From is the version that ran when the rollback was asked for.
	From RestoreFrom `json:"from"`
	// Reverts are the deployments that stop running, newest first: each one's
	// commit is reverted in Git, in this order. Deployments that changed
	// nothing in Git are not listed.
	Reverts []RestoreRevert `json:"reverts,omitempty"`
	// Reason is why the person asked, in their words; required.
	Reason string `json:"reason"`
	// Actor is who asked.
	Actor string `json:"actor"`
	// Emergency says the environment is protected and no approval came
	// first: the rollback is then reviewed afterwards by an approver.
	Emergency bool `json:"emergency,omitempty"`
	// ReviewRequired is Emergency as recorded when asked: the run is on the
	// needs-review list until an approver acknowledges it.
	ReviewRequired bool `json:"review_required,omitempty"`
	// Stable is the extension point for the "stable state" concept (a
	// deployment's version together with the toggles and configuration that
	// ran with it). Nothing sets it yet: a rollback restores the version
	// only, and never a database or configuration.
	Stable *StableState `json:"stable,omitempty"`
}

// StableState is reserved: the version, toggles and configuration known to
// work together. Future work; see docs/guide/rollback-after-success.md.
type StableState struct{}

// RestoreTo is the deployment a rollback restores.
type RestoreTo struct {
	Deployment string `json:"deployment"`
	Version    string `json:"version"`
	Run        string `json:"run"`
	RunNumber  int    `json:"run_number"`
	At         int64  `json:"at,omitempty"` // when it was deployed (Unix ms)
}

// RestoreFrom is what ran when the rollback was asked for.
type RestoreFrom struct {
	Deployment string `json:"deployment"`
	Version    string `json:"version"`
	Run        string `json:"run"`
	RunNumber  int    `json:"run_number"`
}

// RestoreRevert is one deployment to undo.
type RestoreRevert struct {
	Deployment string `json:"deployment"`
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	Run        string `json:"run"`
	RunNumber  int    `json:"run_number"`
}

// IsRestore reports whether the step is the rollback step of a rollback run.
func (s *Step) IsRestore() bool { return s.IsRollback() && s.Restore != nil }

// EventRollback is the trigger event of a rollback run.
const EventRollback = "rollback"

// UsesRestore reports whether the pipeline has the rollback step of a
// rollback run, which servers older than schema 32 cannot read.
func (p *Pipeline) UsesRestore() bool {
	for _, s := range p.Steps {
		if s.IsRestore() {
			return true
		}
	}
	return false
}
