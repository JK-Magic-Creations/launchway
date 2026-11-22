package engine

// JobScope is what a job says about the service containers beside the step and about how the step was sized.
type JobScope struct {
	// ScopedServices says the step named the services it needs (Services; none if empty); the Station then
	// starts only those. Without it every service of Containers is started, as it always was.
	ScopedServices bool     `json:"scoped_services,omitempty"`
	Services       []string `json:"services,omitempty"`
	// Sizing says where the step's memory came from (declared, learned, default, raised); the Station
	// writes it to the step's log. nil: the server does not size this step.
	Sizing *Sizing `json:"sizing,omitempty"`
}
