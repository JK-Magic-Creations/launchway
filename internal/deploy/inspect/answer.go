package inspect

// Answer is what a runner sends back for an inspect request.
type Answer struct {
	Tree    *Tree     `json:"tree,omitempty"`
	Actions []Allowed `json:"actions,omitempty"`
	Text    string    `json:"text,omitempty"`  // a manifest
	Lines   []LogLine `json:"lines,omitempty"` // a log's tail
	Notes   []string  `json:"notes,omitempty"`
	Result  string    `json:"result,omitempty"` // a control's result
	Detail  string    `json:"detail,omitempty"`
}

// LogParams are a logs request's parameters.
type LogParams struct {
	Pod          string `json:"pod,omitempty"`
	Container    string `json:"container,omitempty"`
	ReplicaSet   string `json:"replica_set,omitempty"`
	Role         string `json:"role,omitempty"`
	Job          string `json:"job,omitempty"`
	Previous     bool   `json:"previous,omitempty"`
	Tail         int    `json:"tail,omitempty"`
	SinceSeconds int    `json:"since_seconds,omitempty"`
}

// ControlParams are a control request's parameters.
type ControlParams struct {
	Action   string `json:"action"`
	Object   string `json:"object"`
	Replicas int    `json:"replicas,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ManifestParams are a manifest request's parameters.
type ManifestParams struct {
	Object string `json:"object"`
}
