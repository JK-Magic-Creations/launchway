package config

import "fmt"

// MaintenanceConfig says whether the server itself looks things up at package registries and at the advisory service
// (the versions the dependency updates propose, the advisories the software graph shows).
type MaintenanceConfig struct {
	// Lookups is "server" (the default: the server makes the lookups, and leaves a host alone for an hour after a
	// refused or timed-out connection) or "off" (it makes none: dependency updates propose nothing new and the
	// graph shows no advisories).
	Lookups string `yaml:"lookups"`
}

// Check validates the settings.
func (c MaintenanceConfig) Check() error {
	switch c.Lookups {
	case "", "server", "off":
		return nil
	}
	return fmt.Errorf("maintenance.lookups is server or off, not %q", c.Lookups)
}
