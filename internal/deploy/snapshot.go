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
	// Rights is what the runner found out it may do to the rollout (a
	// self-check against the cluster's authorizer); nil when it did not ask.
	Rights *Rights `json:"rights,omitempty"`
	// Evidence is what the runner saw of the application's pods while it was
	// not healthy: which are crash-looping, with the last lines they logged.
	Evidence []PodEvidence `json:"evidence,omitempty"`
}

// PodEvidence is one pod that is not running well.
type PodEvidence struct {
	Pod       string `json:"pod"`
	Namespace string `json:"namespace,omitempty"`
	// Reason is the container's state reason (CrashLoopBackOff, ImagePullBackOff, Error...).
	Reason   string `json:"reason"`
	Message  string `json:"message,omitempty"`
	Restarts int    `json:"restarts,omitempty"`
	// Crash says the pod is crash-looping: it keeps starting and exiting.
	Crash   bool     `json:"crash,omitempty"`
	LogTail []string `json:"log_tail,omitempty"`
}

// Rights is what a deploy runner may do to a watched rollout, as the
// cluster's authorizer says. A false right comes with Why, the words that
// tell an administrator what to enable. Nil pointers: not checked.
type Rights struct {
	// Control: patch the rollout's status (promote, promote full, abort,
	// retry).
	Control    *bool  `json:"control,omitempty"`
	ControlWhy string `json:"control_why,omitempty"`
	// Logs: read the pods and logs of analysis jobs.
	Logs    *bool  `json:"logs,omitempty"`
	LogsWhy string `json:"logs_why,omitempty"`
	// Write: commit to the GitOps repository the deployment's changes go
	// to, which a rollback needs to revert them.
	Write    *bool  `json:"write,omitempty"`
	WriteWhy string `json:"write_why,omitempty"`
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
	// Leftovers are objects an earlier render made that the release no
	// longer renders and that are still in the cluster, because pruning is off
	// (Argo CD reports them as requiring pruning). AutoPrune says the
	// application prunes by itself; SyncedExceptLeftovers says they are the
	// only thing keeping it OutOfSync.
	Leftovers []Leftover `json:"leftovers,omitempty"`
	// Tracked are the objects of the current render, hooks included, as the
	// application's status and last sync name them; the runner uses them to
	// tell a real leftover from an object owned by the release. Not sent.
	Tracked               []Leftover `json:"-"`
	AutoPrune             bool       `json:"auto_prune,omitempty"`
	SyncedExceptLeftovers bool       `json:"synced_except_leftovers,omitempty"`
	// WindowBlocked, for an application that syncs by itself, says why its
	// AppProject's sync windows do not let it sync now ("" if they do).
	WindowBlocked string `json:"window_blocked,omitempty"`
	// Notes are what the runner could not find out about the application,
	// such as its sync windows; they never decide anything.
	Notes []string `json:"notes,omitempty"`
	// Conditions are the application's error and warning conditions.
	Conditions []string `json:"conditions,omitempty"`
	// Controller is the controller's own account of what it is doing, for the
	// step's log: sync and health, the operation's phase and message, the
	// failing resource or hook with its message. ComparisonError is the
	// text of a ComparisonError condition in full.
	Controller string `json:"controller,omitempty"`
	// RefreshAsked says that the runner asked the controller to re-read the sources (a hard refresh) because it
	// had not noticed the commit; RefreshNeeded says that the runner may not, and what a person should request.
	RefreshAsked    string `json:"refresh_asked,omitempty"`
	RefreshNeeded   string `json:"refresh_needed,omitempty"`
	ComparisonError string `json:"comparison_error,omitempty"`
	// ComparisonPersistent says the runner saw the same ComparisonError on
	// two looks a while apart, so it is not a passing hiccup.
	ComparisonPersistent bool `json:"comparison_persistent,omitempty"`
	// StaleOperation says the sync operation in progress or retrying was
	// started with source revisions the application no longer points at.
	// Cleared says what the deploy runner did about it.
	StaleOperation string `json:"stale_operation,omitempty"`
	Cleared        string `json:"cleared,omitempty"`
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
	// PauseIndefinite says the rollout waits at a pause that no timer ends:
	// a pause step without a duration, a blue-green pause without
	// automatic promotion, or an inconclusive analysis. Someone (or a
	// command) must promote it.
	PauseIndefinite bool `json:"pause_indefinite,omitempty"`
	// PromoteFull says a full promotion was asked for and not yet done.
	PromoteFull bool `json:"promote_full,omitempty"`
	// Revision is the rollout's revision (annotation
	// rollout.argoproj.io/revision): it moves on whenever a pod template
	// becomes current, a reused one included.
	Revision  string `json:"revision,omitempty"`
	Replicas  int    `json:"replicas"`
	Ready     int    `json:"ready"`
	Updated   int    `json:"updated"`
	Available int    `json:"available"`
}

// AnalysisRun is one of a rollout's analysis runs, reduced.
type AnalysisRun struct {
	Name      string `json:"name"`
	Phase     string `json:"phase"` // Pending, Running, Successful, Failed, Error, Inconclusive
	Message   string `json:"message,omitempty"`
	Kind      string `json:"kind,omitempty"` // Step or Background
	StepIndex *int   `json:"step_index,omitempty"`
	PodHash   string `json:"pod_hash,omitempty"`
	// Revision is the rollout's revision the run was made for, and
	// CreatedAt when it was made (Unix ms).
	Revision  string   `json:"revision,omitempty"`
	CreatedAt int64    `json:"created_at,omitempty"`
	Namespace string   `json:"namespace,omitempty"`
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
	// What the metric checks, from the analysis template: the provider
	// (job, prometheus, web, ...), the conditions as written, how often it
	// is measured and how many failures it tolerates.
	Provider         string `json:"provider,omitempty"`
	SuccessCondition string `json:"success_condition,omitempty"`
	FailureCondition string `json:"failure_condition,omitempty"`
	Interval         string `json:"interval,omitempty"`
	FailureLimit     int    `json:"failure_limit"`
	// Measurements are the latest measurements, oldest first.
	Measurements []Measurement `json:"measurements,omitempty"`
	// Job is the Kubernetes Job a job metric ran; JobLog the tail of its
	// log when the metric failed and the runner could get it.
	Job    *JobRef `json:"job,omitempty"`
	JobLog *JobLog `json:"job_log,omitempty"`
}

// Measurement is one measurement of a metric.
type Measurement struct {
	Phase   string `json:"phase"`
	Message string `json:"message,omitempty"`
	Value   string `json:"value,omitempty"`
	At      int64  `json:"at,omitempty"` // when it finished (Unix ms), else started
}

// JobRef names the Job a job metric ran.
type JobRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// JobLog is the end of a failed analysis job's log, or why there is none.
type JobLog struct {
	Lines []string `json:"lines,omitempty"`
	// Note says why Lines is empty, or where they came from.
	Note string `json:"note,omitempty"`
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
	// Until and Pauses, when set, replace Rollout's: they are a rollout
	// step's (see pipeline.Step). PastStep, for a rollout step that
	// promoted, is the canary step it promoted: a pause there does not end
	// until: paused, only a later one does.
	Until    string `json:"until,omitempty"`
	Pauses   string `json:"pauses,omitempty"`
	PastStep *int   `json:"past_step,omitempty"`
	// Leftovers is fail or warn (see pipeline.Sync); Prune says the step
	// asks for pruning.
	Leftovers string `json:"leftovers,omitempty"`
	Prune     bool   `json:"prune,omitempty"`
	// Refresh is ask or never (see pipeline.Sync): whether the runner may ask the controller to re-read the
	// application's sources while the commit is not noticed. Empty means ask.
	Refresh string `json:"refresh,omitempty"`
}

// Leftover is an object from an earlier render that is still in the cluster.
type Leftover struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Security says the kind widens access if it stays (network policies,
	// roles and bindings, service accounts, ingress and route kinds, secrets).
	Security bool `json:"security,omitempty"`
}

// securityKinds are the kinds whose leftover objects can widen access.
var securityKinds = map[string]bool{
	"NetworkPolicy": true, "CiliumNetworkPolicy": true, "CiliumClusterwideNetworkPolicy": true,
	"Role": true, "RoleBinding": true, "ClusterRole": true, "ClusterRoleBinding": true,
	"ServiceAccount": true, "Ingress": true, "HTTPRoute": true, "GRPCRoute": true, "TCPRoute": true,
	"TLSRoute": true, "Gateway": true, "IngressRoute": true, "Secret": true,
}

// String words a leftover as Kind/name.
func (l Leftover) String() string { return l.Kind + "/" + l.Name }
