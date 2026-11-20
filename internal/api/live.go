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
	// MCP turns on the Model Context Protocol endpoint, /mcp, and sets its
	// origins, limits and OAuth; nil is off.
	MCP *MCPOptions
}

// mcpOpts returns the MCP settings in force: nil when the endpoint is off.
func (s *Server) mcpOpts() *MCPOptions {
	if l := s.live.Load(); l != nil {
		return l.MCP
	}
	return s.MCP
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
	l := &Live{MCP: s.MCP, Drafts: s.Drafts, RunnerPools: s.RunnerPools, WaiveRole: s.WaiveRole, RunnersRole: s.RunnersRole, TokenMaxDays: s.TokenMaxDays}
	if s.Auth != nil {
		l.Policy = s.Auth.Policy
	}
	return l
}

// mcpNow is mcpOpts for code that runs once the endpoint has been found
// on: if it was turned off meanwhile, the settings are empty ones, which
// ask for nothing.
func (s *Server) mcpNow() *MCPOptions {
	if m := s.mcpOpts(); m != nil {
		return m
	}
	return &MCPOptions{}
}
