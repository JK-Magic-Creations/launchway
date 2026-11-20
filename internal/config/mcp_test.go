package config

import (
	"strings"
	"testing"
)

func TestMCPSettings(t *testing.T) {
	if err := loadFile(t, "", false); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]string{
		"mcp: { enabled: false }\n":                                                "",
		"mcp: { allowed_origins: [https://tools.example.com] }\n":                  "",
		"mcp: { allowed_origins: [tools.example.com] }\n":                          "not an origin",
		"mcp: { allowed_origins: [\"https://tools.example.com/page\"] }\n":         "not an origin",
		"mcp: { concurrent: -1 }\n":                                                "not a sensible limit",
		"mcp: { concurrent: 8, total: 4 }\n":                                       "may not be more",
		"mcp: { oauth: { resource: https://launchway.example.com/mcp } }\n":        "needs sign-in",
		"mcp: { oauth: { scopes: [a] } }\n":                                        "say this server's MCP address",
		"mcp: { oauth: { resource: https://x.example/mcp, scopes: [\"a b\"] } }\n": "not a scope",
		"mcp: { enable: false }\n":                                                 "field enable not found",
	} {
		err := loadFile(t, body, false)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", body, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
}
