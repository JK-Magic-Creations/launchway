package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/config"
	"github.com/jk-magic-creations/launchway/internal/settings"
)

// The MCP endpoint's settings are the installation's, in the database: an
// administrator turns the endpoint off and on, and changes its limits,
// while the server runs.
func TestMCPSettingsChangeWhileRunning(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	// What main does: apply the composed configuration to the server.
	apply := func(c *config.Config, _ *settings.Layout) error {
		l := *e.api.cur()
		l.MCP = nil
		if m := c.MCP; m.On() {
			l.MCP = &MCPOptions{AllowedOrigins: m.AllowedOrigins, RequestsPerMinute: m.RequestsPerMinute, Concurrent: m.Concurrent, Total: m.Total}
		}
		e.api.Reconfigure(l)
		return nil
	}
	if err := e.api.Settings.Load(apply); err != nil {
		t.Fatal(err)
	}
	ada := e.signIn(t, "ada")
	tok := e.tokenFor(t, "ada", "live")
	ping := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	hdr := append([]string{"Content-Type", "application/json", "Accept", "application/json, text/event-stream"}, bearer(tok)...)
	if r := rawPost(t, e.mcpURL(), ping, hdr...); r.status != http.StatusOK {
		t.Fatalf("on: %d %s", r.status, r.body)
	}
	inst := e.srv.URL + "/api/v1/config/installation/installation"
	if r := ada.do(http.MethodPut, inst, `{"spec":{"mcp":{"enabled":false}},"comment":"agents are not ready"}`); r.status != 200 && r.status != 201 {
		t.Fatalf("turn off: %d %s", r.status, r.body)
	}
	if r := rawPost(t, e.mcpURL(), ping, hdr...); r.status != http.StatusNotFound {
		t.Fatalf("after turning it off: %d %s", r.status, r.body)
	}
	if r := ada.do(http.MethodPut, inst, `{"spec":{"mcp":{"requests_per_minute":2000}}}`); r.status != 200 {
		t.Fatalf("turn on: %d %s", r.status, r.body)
	}
	if r := rawPost(t, e.mcpURL(), ping, hdr...); r.status != http.StatusOK {
		t.Fatalf("after turning it on: %d %s", r.status, r.body)
	}
	// An OAuth resource that is not this server's MCP address is refused
	// when it is set, not when the server next starts.
	r := ada.do(http.MethodPut, inst, `{"spec":{"mcp":{"oauth":{"resource":"https://launchway.example.test/api"}}}}`)
	if r.status != 400 || !strings.Contains(string(r.body), "/mcp") {
		t.Errorf("a wrong OAuth resource: %d %s", r.status, r.body)
	}
	h := ada.get(inst + "/history")
	if h.status != 200 || !strings.Contains(string(h.body), "agents are not ready") {
		t.Errorf("history: %d %s", h.status, h.body)
	}
}
