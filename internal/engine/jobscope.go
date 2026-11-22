package engine

// JobScope is what a job says about the service containers beside the step and about how the step was sized.
type JobScope struct {
	// ScopedServices says the step named the services it needs (Services; none if empty); the Station then
	// starts only those. Without it every service of Containers is started, as it always was.
	ScopedServices bool     `json:"scoped_services,omitempty"`
	Services       []string `json:"services,omitempty"`
	// RunServices is, for a runner that makes one pod for the whole run (kubernetes.pod: per-run), the services
	// the run's steps need together: the services any step names, or all of them when some step names none.
	// RunScopedServices says the list is to be used (every step that runs in a container named its services).
	RunScopedServices bool     `json:"run_scoped_services,omitempty"`
	RunServices       []string `json:"run_services,omitempty"`
	// Sizing says where the step's memory came from (declared, learned, default, raised); the Station
	// writes it to the step's log. nil: the server does not size this step.
	Sizing *Sizing `json:"sizing,omitempty"`
}
