package engine

// EnvironmentView is an environment with what Launchway knows runs there.
type EnvironmentView struct {
	Name     string     `json:"name"`
	Target   string     `json:"target"`
	Queue    string     `json:"queue"`
	Projects []string   `json:"projects"`
	Services []UnitView `json:"services"`
	// Protected: see Environment.Protected.
	Protected bool `json:"protected,omitempty"`
	// MinApprovals: see Environment.MinApprovals.
	MinApprovals int `json:"min_approvals,omitempty"`
	// Observers says which runner observes the environment's target, and
	// until when (Unix ms); empty when none does.
	Observer        string `json:"observer,omitempty"`
	ObserverExpires int64  `json:"observer_expires,omitempty"`
}

// UnitView is one service of a project in an environment.
type UnitView struct {
	Project string `json:"project"`
	Service string `json:"service"`
	// Current is what Launchway last deployed there and left running.
	Current *CurrentView `json:"current,omitempty"`
	// Lock is the deployment holding the lock, if any.
	Lock *DeployRef `json:"lock,omitempty"`
	// Waiting are the runs waiting for the lock.
	Waiting []DeployRef `json:"waiting"`
	// Last is the newest deployment, with what was last observed.
	Last *DeploymentView `json:"last,omitempty"`
	// Live is what deploy runners last saw of the service while nothing
	// was deploying it, and whether it differs from Current (schema 14).
	Live *LiveView `json:"live,omitempty"`
	// Rollback is the rollback action for the service as the server reports
	// it (schema 32): available with its targets, or why not.
	Rollback *Action `json:"rollback,omitempty"`
}

// CurrentView is what an environment runs, as far as Launchway knows.
type CurrentView struct {
	Deployment string `json:"deployment"`
	Version    string `json:"version"`
	Run        string `json:"run"`
	RunNumber  int    `json:"run_number"`
	Since      int64  `json:"since"`
}
