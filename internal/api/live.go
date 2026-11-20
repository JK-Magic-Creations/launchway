package api

import (
	"github.com/jk-magic-creations/launchway/internal/auth"
)

// Live is the part of the server's settings an administrator manages, kept
// in the database since schema 23: who has which role, who may start draft
// runs, the deploy pools' tokens, and the roles needed to waive rules and
// manage runners. Reconfigure replaces it while the server runs.
type Live struct {
	// Policy is the role bindings; used only when sign-in is on.
	Policy       *auth.Policy
	Drafts       *auth.DraftRules
	RunnerPools  []RunnerPool
	WaiveRole    *auth.Role
	RunnersRole  *auth.Role
	TokenMaxDays int
}

// Reconfigure replaces the live settings. Requests in flight finish with
// what they started with.
func (s *Server) Reconfigure(l Live) { s.live.Store(&l) }

// cur returns the live settings: those given to Reconfigure, or, until it is
// called, the Server's own fields.
func (s *Server) cur() *Live {
	if l := s.live.Load(); l != nil {
		return l
	}
	l := &Live{Drafts: s.Drafts, RunnerPools: s.RunnerPools, WaiveRole: s.WaiveRole, RunnersRole: s.RunnersRole, TokenMaxDays: s.TokenMaxDays}
	if s.Auth != nil {
		l.Policy = s.Auth.Policy
	}
	return l
}
