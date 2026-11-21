package engine

import "encoding/json"

// DeploymentView is a deployment as the run page and the Environments page
// show it.
type DeploymentView struct {
	ID              string          `json:"id"`
	RunID           string          `json:"run_id"`
	RunNumber       int             `json:"run_number"`
	Project         string          `json:"project"`
	Step            string          `json:"step"`
	RollbackStep    string          `json:"rollback_step,omitempty"`
	Environment     string          `json:"environment"`
	Service         string          `json:"service"`
	Target          string          `json:"target"`
	Application     string          `json:"application"`
	Mode            string          `json:"mode"`
	Version         string          `json:"version"`
	PreviousID      string          `json:"previous_id,omitempty"`
	PreviousVersion string          `json:"previous_version,omitempty"`
	Repo            string          `json:"repo,omitempty"`
	Branch          string          `json:"branch,omitempty"`
	Commit          string          `json:"commit,omitempty"`
	Changed         bool            `json:"changed"`
	Revert          string          `json:"revert,omitempty"`
	Phase           string          `json:"phase"`
	Detail          string          `json:"detail"`
	Verdict         string          `json:"verdict,omitempty"`
	OpenedAt        int64           `json:"opened_at"`
	DeployedAt      int64           `json:"deployed_at,omitempty"`
	ClosedAt        int64           `json:"closed_at,omitempty"`
	Snapshot        json.RawMessage `json:"snapshot,omitempty"`
	ObservedAt      int64           `json:"observed_at,omitempty"`
	ObservedBy      string          `json:"observed_by,omitempty"`
	Holding         bool            `json:"holding"` // it holds its environment's lock
	Timeline        []TimelineEntry `json:"timeline,omitempty"`
	// Schema 14: the deployment's pod template hash as first seen in its
	// rollout, the commands given to the rollout, and the runner observing
	// its target (empty when none does).
	PodHash         string        `json:"pod_hash,omitempty"`
	Commands        []CommandView `json:"commands,omitempty"`
	Observer        string        `json:"observer,omitempty"`
	ObserverExpires int64         `json:"observer_expires,omitempty"`
	// Rollback (schema 32), for a deployment made by a rollback run, says
	// what it restored and who asked; RolledBackBy lists the rollback runs
	// that undid this deployment; Actions say what a person may do with it:
	// roll back to what ran before it, or why not.
	Rollback     *DeploymentRollback `json:"rollback,omitempty"`
	RolledBackBy []RunRef            `json:"rolled_back_by,omitempty"`
	Actions      []Action            `json:"actions,omitempty"`
}

// TimelineEntry is one change in what was observed of a deployment.
type TimelineEntry struct {
	At     int64  `json:"at"`
	Step   string `json:"step"`
	Phase  string `json:"phase"`
	Detail string `json:"detail"`
}
