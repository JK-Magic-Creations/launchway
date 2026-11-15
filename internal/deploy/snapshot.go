// Package deploy holds what the server and deploy runners share about a
// deployment being watched: the reduced, typed snapshot a runner reports of
// an Argo CD application and its Argo Rollouts rollout, the functions that
// reduce the cluster's objects to it, and Evaluate, which decides from a
// snapshot whether a deployment is done, failed or still going.
//
// A snapshot never carries manifests or anything secret: only states,
// revisions, counts and the messages Argo CD and Argo Rollouts write in
// their status.
package deploy

import "github.com/jk-magic-creations/launchway/internal/pipeline"

// Snapshot is what a deploy runner saw of one deployment at one moment.
type Snapshot struct {
	// Error says why the runner could not see the application at all, for
	// example because it cannot reach the cluster's API. The rest is then
	// empty.
	Error    string        `json:"error,omitempty"`
	App      *App          `json:"app,omitempty"`
	Rollout  *Rollout      `json:"rollout,omitempty"`
	Analysis []AnalysisRun `json:"analysis,omitempty"`
}

// App is an Argo CD application, reduced.
type App struct {
	Found bool `json:"found"`
	// SourceError says that the application has no source from the
	// deployment's repository, so nothing it deploys can be compared with the
	// commit.
	SourceError string `json:"source_error,omitempty"`
	Sync        string `json:"sync"`   // Synced, OutOfSync or Unknown
	Health      string `json:"health"` // Healthy, Progressing, Degraded, Suspended, Missing or Unknown
	// HealthMessage is Argo CD's message for the health, if any.
	HealthMessage string `json:"health_message,omitempty"`
	// Revision is the commit Argo CD compared the live state with, for the
	// source from the deployment's repository; Contains says whether it
	// includes the deployment's commit (nil when that could not be checked).
	Revision string `json:"revision,omitempty"`
	Contains *bool  `json:"contains,omitempty"`
	// Source is which of a multi-source application's sources that is (0
	// for a single source).
	Source    int        `json:"source"`
	Automated bool       `json:"automated"`
	Operation *Operation `json:"operation,omitempty"`
	// ReconciledAt is when Argo CD last compared the application (Unix ms).
	ReconciledAt int64 `json:"reconciled_at,omitempty"`
	// Resources are the resources the deployment judges, as Argo CD reports
	// them: those listed under sync.resources, else none (the application's
	// own sync and health then decide).
	Resources []Resource `json:"resources,omitempty"`
	// WindowBlocked, for an application that syncs by itself, says why its
	// AppProject's sync windows do not let it sync now ("" if they do).
	WindowBlocked string `json:"window_blocked,omitempty"`
	// Notes are what the runner could not find out about the application,
	// such as its sync windows; they never decide anything.
	Notes []string `json:"notes,omitempty"`
	// Conditions are the application's error and warning conditions.
	Conditions []string `json:"conditions,omitempty"`
}

// Operation is an application's current or last sync operation.
type Operation struct {
	Phase    string `json:"phase"` // Running, Terminating, Succeeded, Failed or Error
	Message  string `json:"message,omitempty"`
	Revision string `json:"revision,omitempty"` // of the deployment's source
	// Contains says whether Revision includes the deployment's commit.
	Contains   *bool `json:"contains,omitempty"`
	StartedAt  int64 `json:"started_at,omitempty"`
	FinishedAt int64 `json:"finished_at,omitempty"`
}

// Resource is one resource of an application, as Argo CD reports it.
type Resource struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Found     bool   `json:"found"`
	Sync      string `json:"sync,omitempty"`
	Health    string `json:"health,omitempty"`
	Message   string `json:"message,omitempty"`
}

// Rollout is an Argo Rollouts rollout, reduced.
type Rollout struct {
	Found   bool   `json:"found"`
	Phase   string `json:"phase,omitempty"` // Healthy, Progressing, Paused, Degraded
	Message string `json:"message,omitempty"`
	// Generations: ObservedKnown says the controller has observed the
	// rollout's current spec, and, for a rollout that takes its pod template
	// from a workload (workloadRef), that workload's too.
	Generation         int64  `json:"generation"`
	ObservedGeneration string `json:"observed_generation,omitempty"`
	ObservedKnown      bool   `json:"observed"`
	WorkloadRef        bool   `json:"workload_ref,omitempty"`
	// WorkloadUnread says the rollout takes its pod template from a
	// workload the runner could not read, so whether the controller has
	// seen its latest version is unknown.
	WorkloadUnread bool   `json:"workload_unread,omitempty"`
	CurrentPodHash string `json:"current_pod_hash,omitempty"`
	StableRS       string `json:"stable_rs,omitempty"`
	Strategy       string `json:"strategy,omitempty"` // canary or blueGreen
	// Step is the index of the current canary step (nil when there are
	// none), of Steps.
	Step         *int     `json:"step,omitempty"`
	Steps        int      `json:"steps"`
	StepNames    []string `json:"step_names,omitempty"`
	CanaryWeight *int     `json:"canary_weight,omitempty"`
	Paused       bool     `json:"paused"`
	PauseReasons []string `json:"pause_reasons,omitempty"`
	// PauseUntil is when a timed pause ends (Unix ms), when known.
	PauseUntil int64 `json:"pause_until,omitempty"`
	Aborted    bool  `json:"aborted"`
	Replicas   int   `json:"replicas"`
	Ready      int   `json:"ready"`
	Updated    int   `json:"updated"`
	Available  int   `json:"available"`
}

// AnalysisRun is one of a rollout's analysis runs, reduced.
type AnalysisRun struct {
	Name      string   `json:"name"`
	Phase     string   `json:"phase"` // Pending, Running, Successful, Failed, Error, Inconclusive
	Message   string   `json:"message,omitempty"`
	Kind      string   `json:"kind,omitempty"` // Step or Background
	StepIndex *int     `json:"step_index,omitempty"`
	PodHash   string   `json:"pod_hash,omitempty"`
	Metrics   []Metric `json:"metrics,omitempty"`
}

// Metric is one metric of an analysis run.
type Metric struct {
	Name         string `json:"name"`
	Phase        string `json:"phase"`
	Message      string `json:"message,omitempty"`
	Count        int    `json:"count"`
	Successful   int    `json:"successful"`
	Failed       int    `json:"failed"`
	Inconclusive int    `json:"inconclusive"`
	Error        int    `json:"error"`
}

// Spec is what a deployment is watched for: it comes from the deploy step
// and the action that started the watch.
type Spec struct {
	// Kind is deploy or rollback.
	Kind string `json:"kind"`
	// Commit must be included in the revision Argo CD has synced: the
	// deployment's commit, or for a rollback the revert's.
	Commit string `json:"commit"`
	// Health is application or resources (see pipeline.ArgoCD).
	Health    string               `json:"health"`
	Resources []pipeline.Resource  `json:"resources,omitempty"`
	Rollout   *pipeline.RolloutRef `json:"rollout,omitempty"`
	// Windows is wait or fail (see pipeline.Sync).
	Windows string `json:"windows"`
}
