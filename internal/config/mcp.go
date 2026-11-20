package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// MCPConfig controls the Model Context Protocol endpoint, /mcp, through
// which agents use Launchway with API tokens. It is on unless turned off:
// it gives no rights that the token does not already give over the API.
type MCPConfig struct {
	// Enabled turns the endpoint off when false; default true.
	Enabled *bool `yaml:"enabled"`
	// AllowedOrigins are browser origins, besides this server's own, whose
	// pages may call /mcp: scheme://host[:port].
	AllowedOrigins []string `yaml:"allowed_origins"`
	// RequestsPerMinute per token, on each server; default 120.
	RequestsPerMinute int `yaml:"requests_per_minute"`
	// Concurrent requests per token, on each server; default 4.
	Concurrent int `yaml:"concurrent"`
	// Total concurrent MCP requests on each server; default 32.
	Total int `yaml:"total"`
	// OAuth, if set, also accepts the sign-in provider's access tokens
	// issued for this endpoint.
	OAuth *MCPOAuthConfig `yaml:"oauth"`
}

// MCPOAuthConfig says which of the provider's access tokens /mcp accepts.
type MCPOAuthConfig struct {
	// Resource is the endpoint's address, as clients and tokens name it:
	// https://launchway.example.com/mcp.
	Resource string `yaml:"resource"`
	// Scopes a token must carry, if any.
	Scopes []string `yaml:"scopes"`
}

// On reports whether the endpoint is on.
func (m MCPConfig) On() bool { return m.Enabled == nil || *m.Enabled }

// Check checks the settings.
func (m MCPConfig) Check() error {
	for _, o := range m.AllowedOrigins {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return fmt.Errorf("mcp.allowed_origins: %q is not an origin such as https://tools.example.com", o)
		}
	}
	for name, v := range map[string]int{"requests_per_minute": m.RequestsPerMinute, "concurrent": m.Concurrent, "total": m.Total} {
		if v < 0 || v > 100000 {
			return fmt.Errorf("mcp.%s: %d is not a sensible limit", name, v)
		}
	}
	if m.Concurrent > 0 && m.Total > 0 && m.Concurrent > m.Total {
		return errors.New("mcp.concurrent may not be more than mcp.total")
	}
	if o := m.OAuth; o != nil {
		if strings.TrimSpace(o.Resource) == "" {
			return errors.New("mcp.oauth.resource: say this server's MCP address, such as https://launchway.example.com/mcp")
		}
		for _, s := range o.Scopes {
			if s == "" || strings.ContainsAny(s, " \"\\") {
				return fmt.Errorf("mcp.oauth.scopes: %q is not a scope", s)
			}
		}
	}
	return nil
}
