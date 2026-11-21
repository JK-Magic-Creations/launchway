// Package inspect is what a deploy runner shows of one delivery: the live
// tree of the objects the release owns, their manifests and events, their
// logs, the rollout's state, and the few controls a person may use on it.
//
// Everything here runs on the deploy runner, which has scoped access to the
// namespaces its Airfield lists. The server never reaches the cluster: it
// asks the runner (a narrow "inspect" request) and caches what comes back
// for a few seconds.
//
// Secret values never leave this package: a Secret shows its name, type and
// key names; ConfigMap data, manifests and logs pass through Redactor.
package inspect

// Roles a ReplicaSet (or the Service selecting it) has in a delivery.
const (
	RoleStable  = "stable"
	RoleCanary  = "canary"
	RolePreview = "preview"
	RoleActive  = "active"
	RoleOld     = "old"     // an earlier revision, scaled down but kept
	RoleCurrent = "current" // a plain Deployment's newest ReplicaSet
)

// What changed in this Launch.
const (
	ChangeNew       = "new"
	ChangeChanged   = "changed"
	ChangeUnchanged = "unchanged"
	ChangeLeftover  = "leftover" // carries the release's label but is not rendered any more
)

// Health of a node, in Argo CD's words (plus Completed for finished jobs).
const (
	Healthy     = "Healthy"
	Progressing = "Progressing"
	Degraded    = "Degraded"
	Suspended   = "Suspended"
	Missing     = "Missing"
	Completed   = "Completed"
	Unknown     = "Unknown"
)

// Ref names one object.
type Ref struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// ID is the stable identifier of a node: kind/namespace/name.
func (r Ref) ID() string { return r.Kind + "/" + r.Namespace + "/" + r.Name }

// Tree is the live tree of a delivery's objects. Nodes are flat; each names
// its parent, so a page of 300 pods costs one list.
type Tree struct {
	Application string   `json:"application,omitempty"`
	Namespaces  []string `json:"namespaces"`
	At          int64    `json:"at"` // when the runner built it (Unix ms)
	// Since is the start of the Launch the changes are measured against.
	Since int64  `json:"since,omitempty"`
	Nodes []Node `json:"nodes"`
	// Notes say what could not be read and why, in plain words (a missing
	// permission names the chart value that grants it).
	Notes []string `json:"notes,omitempty"`
	// Truncated says the tree was cut at the node limit.
	Truncated bool `json:"truncated,omitempty"`
	// Rollout is the progressive delivery's state, when there is one.
	Rollout *RolloutView `json:"rollout,omitempty"`

	red *Redactor // hides this release's Secret values in everything shown
}

// Node is one object of the tree.
type Node struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Parent    string `json:"parent,omitempty"`
	// Section places a node that has no parent object: workloads, network,
	// config, access, scaling, storage, testing.
	Section string `json:"section,omitempty"`

	Health   string `json:"health"`
	Message  string `json:"message,omitempty"` // the controller's message
	Created  int64  `json:"created,omitempty"` // Unix ms
	Change   string `json:"change,omitempty"`
	Role     string `json:"role,omitempty"`     // ReplicaSets, Services
	Revision string `json:"revision,omitempty"` // ReplicaSets, workloads
	Hash     string `json:"hash,omitempty"`     // pod template hash

	Replicas *Replicas `json:"replicas,omitempty"`
	// Containers of a pod.
	Containers []Container `json:"containers,omitempty"`
	// NotReady says why a pod is not ready: status, events and the last log
	// lines, gathered.
	NotReady *NotReady `json:"not_ready,omitempty"`
	// Keys of a ConfigMap or Secret; Data is a ConfigMap's values, redacted
	// and clipped. A Secret has no Data, ever.
	Keys []string          `json:"keys,omitempty"`
	Data map[string]string `json:"data,omitempty"`
	// Selects, for a Service, are the ReplicaSets its selector matches;
	// Ports its ports.
	Selects []string `json:"selects,omitempty"`
	Ports   []string `json:"ports,omitempty"`
	// Weights, for a route, are its backends and their weights.
	Weights []Weight `json:"weights,omitempty"`
	// Bound lists the objects a ServiceAccount, Role or binding refers to.
	Bound []string `json:"bound,omitempty"`
	// UsedBy, for a ConfigMap or Secret, are the workloads that mount or
	// read it.
	UsedBy []string `json:"used_by,omitempty"`
	Events []Event  `json:"events,omitempty"`
	// Labels of the object that identify the release (a few).
	Image string `json:"image,omitempty"`
}

// Replicas are a workload's or ReplicaSet's replica counts.
type Replicas struct {
	Desired   int `json:"desired"`
	Ready     int `json:"ready"`
	Available int `json:"available"`
	Updated   int `json:"updated"`
}

// Container is one container of a pod.
type Container struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Init     bool   `json:"init,omitempty"`
	Ready    bool   `json:"ready"`
	Restarts int    `json:"restarts"`
	State    string `json:"state"`            // running, waiting, terminated
	Reason   string `json:"reason,omitempty"` // CrashLoopBackOff, OOMKilled, ...
	ExitCode *int   `json:"exit_code,omitempty"`
	// Previous says a terminated instance's log can be read.
	Previous bool `json:"previous,omitempty"`
}

// NotReady is the answer to "why is this pod not ready".
type NotReady struct {
	Why      string   `json:"why"`
	LastLogs []string `json:"last_logs,omitempty"`
}

// Weight is one route backend.
type Weight struct {
	Backend string `json:"backend"`
	Weight  int    `json:"weight"`
	Match   string `json:"match,omitempty"` // a header or host the rule applies to
}

// Event is a Kubernetes event about an object.
type Event struct {
	Type    string `json:"type"` // Warning or Normal
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int    `json:"count"`
	Last    int64  `json:"last"` // Unix ms
}

// RolloutView is the progressive delivery as a person follows it.
type RolloutView struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`     // Rollout, Deployment
	Strategy  string `json:"strategy"` // canary, blueGreen, rolling
	Phase     string `json:"phase"`
	Message   string `json:"message,omitempty"`
	Paused    bool   `json:"paused"`
	// UserPaused says a person paused it (spec.paused), as opposed to a
	// pause step waiting.
	UserPaused bool `json:"user_paused,omitempty"`
	Aborted    bool `json:"aborted"`
	// Steps are the strategy's steps in order; Current is the index of the
	// one the rollout is at (-1: not started or finished).
	Steps   []Step `json:"steps"`
	Current int    `json:"current"`
	// Desired and actual weight of the canary, in percent; -1 unknown.
	DesiredWeight int `json:"desired_weight"`
	ActualWeight  int `json:"actual_weight"`
	// ActualSource says how ActualWeight was known: "traffic router" (the
	// controller's report of the route) or "replica ratio" (computed).
	ActualSource string `json:"actual_source,omitempty"`
	// ActiveService, PreviewService, StableService and CanaryService are the
	// services the strategy names.
	Services map[string]string `json:"services,omitempty"`
	// PauseStarted and PauseEnds (Unix ms) time the current pause; PauseEnds
	// is 0 for a pause nobody ends but a person.
	PauseStarted int64 `json:"pause_started,omitempty"`
	PauseEnds    int64 `json:"pause_ends,omitempty"`
	// PreviewHost / PreviewHeader: how a person reaches the new version
	// without live traffic, when the strategy has such a route.
	PreTraffic *PreTraffic `json:"pre_traffic,omitempty"`
	Analysis   []Analysis  `json:"analysis,omitempty"`
}

// PreTraffic says how the new version is reachable while it takes none of
// the real traffic.
type PreTraffic struct {
	// Address is the preview address: a Service, or a header/host route.
	Address string `json:"address"`
	// LiveWeight is the share of real traffic the new version has now.
	LiveWeight int `json:"live_weight"`
}

// Step is one step of the strategy.
type Step struct {
	Index int    `json:"index"`
	Kind  string `json:"kind"` // setWeight, pause, analysis, experiment, setCanaryScale, setHeaderRoute, ...
	Label string `json:"label"`
	// Weight, for setWeight; Seconds, for a timed pause (0: until promoted).
	Weight  *int   `json:"weight,omitempty"`
	Seconds int    `json:"seconds,omitempty"`
	State   string `json:"state"` // done, current, next
}

// Analysis is an analysis run with its metrics, for the page.
type Analysis struct {
	Name    string   `json:"name"`
	Phase   string   `json:"phase"`
	Message string   `json:"message,omitempty"`
	Metrics []Metric `json:"metrics"`
	Jobs    []string `json:"jobs,omitempty"` // node ids of the Jobs it ran
	Created int64    `json:"created,omitempty"`
}

// Metric is one measured thing: its threshold and each measurement.
type Metric struct {
	Name     string        `json:"name"`
	Provider string        `json:"provider,omitempty"`
	Query    string        `json:"query,omitempty"` // what is measured, as written
	Success  string        `json:"success,omitempty"`
	Failure  string        `json:"failure,omitempty"`
	Phase    string        `json:"phase"`
	Points   []Measurement `json:"points,omitempty"`
}

// Measurement is one point of a metric.
type Measurement struct {
	At    int64  `json:"at,omitempty"`
	Phase string `json:"phase"`
	Value string `json:"value,omitempty"`
	Note  string `json:"note,omitempty"`
}
