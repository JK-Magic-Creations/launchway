package api

// The MCP tool for built-in accounts: read-only on purpose. People's
// credentials are issued in the browser or at the server's console and never
// through a token or an agent, so that no token can mint a new person.

import (
	"context"
	"net/http"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addUserTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_users", Annotations: readOnly("List the built-in accounts"),
		Description: "The built-in accounts of this installation (administrators only): user name, role, state (active, invited, locked or disabled), whether a second factor is set up, and the last sign-in. Never any password, secret or code."},
		mcpCallTimeout, listUsers)
}

type usersOut struct {
	Summary          string     `json:"summary"`
	Enabled          bool       `json:"enabled"`
	SecondFactorRule string     `json:"second_factor_rule,omitempty"`
	Users            []userView `json:"users"`
}

func listUsers(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (usersOut, error) {
	var raw struct {
		Users   []userView `json:"users"`
		Enabled bool       `json:"enabled"`
		Rule    string     `json:"second_factor_rule"`
	}
	if err := c.apiJSON(ctx, "list_users", http.MethodGet, "/api/v1/users", nil, &raw); err != nil {
		return usersOut{}, err
	}
	for i := range raw.Users {
		raw.Users[i].DisplayName = untrusted(raw.Users[i].DisplayName, 100)
		raw.Users[i].Email = untrusted(raw.Users[i].Email, 100)
	}
	if raw.Users == nil {
		raw.Users = []userView{}
	}
	return usersOut{Summary: pluralN(len(raw.Users), "account"), Enabled: raw.Enabled, SecondFactorRule: raw.Rule, Users: raw.Users}, nil
}

func addServiceAccountTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_service_accounts", Annotations: readOnly("List the service accounts"),
		Description: "The service accounts of this installation (administrators only): name, role, whether it is switched off. They are non-person accounts that own tokens for automation. Making, changing and removing them, and making their tokens, is done by an administrator through the API or `launchway service-accounts`, never through a token or an agent."},
		mcpCallTimeout, listServiceAccountsTool)
}

type serviceAccountsOut struct {
	Summary         string                  `json:"summary"`
	ServiceAccounts []engine.ServiceAccount `json:"service_accounts"`
}

func listServiceAccountsTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (serviceAccountsOut, error) {
	var raw struct {
		ServiceAccounts []engine.ServiceAccount `json:"service_accounts"`
	}
	if err := c.apiJSON(ctx, "list_service_accounts", http.MethodGet, "/api/v1/service-accounts", nil, &raw); err != nil {
		return serviceAccountsOut{}, err
	}
	for i := range raw.ServiceAccounts {
		raw.ServiceAccounts[i].Description = untrusted(raw.ServiceAccounts[i].Description, 300)
	}
	if raw.ServiceAccounts == nil {
		raw.ServiceAccounts = []engine.ServiceAccount{}
	}
	return serviceAccountsOut{Summary: pluralN(len(raw.ServiceAccounts), "service account"), ServiceAccounts: raw.ServiceAccounts}, nil
}
