package fleet

import (
	"context"
)

// Member is what a traffic provider reports about one host.
type Member struct {
	Member   string `json:"member"` // backend/server
	State    string `json:"state"`  // ready, drain, maint, down, up
	Weight   int    `json:"weight"`
	Sessions int    `json:"sessions"`
	Health   string `json:"health,omitempty"`
}

// Traffic is a traffic provider: it moves what reaches hosts. The verbs are
// the shifter's (doc 46 section 2.3) for hosts.
type Traffic interface {
	// Drain takes the host out of new-connection rotation; what is in flight
	// finishes.
	Drain(ctx context.Context, member string) error
	// Admit puts a drained host back in rotation.
	Admit(ctx context.Context, member string) error
	// SetWeight sets the member's weight (0 sends it nothing new).
	SetWeight(ctx context.Context, member string, weight int) error
	// Observe reads every member.
	Observe(ctx context.Context) (map[string]Member, error)
}
