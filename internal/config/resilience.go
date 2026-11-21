package config

import (
	"fmt"
	"time"
)

// ResilienceConfig is how the server rides through a brief database outage.
type ResilienceConfig struct {
	// StallBudget is how long a request waits for a database that is
	// briefly gone (a failover, a restart) before it is answered with 503
	// and Retry-After; default 120s.
	StallBudget string `yaml:"stall_budget"`
}

// StallBudget returns the stall budget, or the default of two minutes.
func (c *Config) StallBudget() (time.Duration, error) {
	if c.Resilience.StallBudget == "" {
		return 2 * time.Minute, nil
	}
	d, err := time.ParseDuration(c.Resilience.StallBudget)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("resilience.stall_budget: %q is not a duration such as 120s", c.Resilience.StallBudget)
	}
	return d, nil
}
