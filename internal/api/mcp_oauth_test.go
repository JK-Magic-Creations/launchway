package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

// With mcp.oauth set, /mcp is an OAuth resource server for the sign-in
// provider: it publishes its metadata, points clients at it on 401, and
// accepts the provider's access tokens issued for it, with the rights of
// the person they are for. Launchway API tokens keep working.
func TestMCPOAuthResourceServer(t *testing.T) {
	const resource = "https://launchway.example.com/mcp"
	e := mcpEnv(t, envOpts{mcp: &MCPOptions{OAuth: &MCPOAuth{Resource: resource, Scopes: []string{"launchway:mcp"}}}})
	if err := CheckMCPOAuth(e.api.MCP.OAuth, e.api.Auth); err != nil {
		t.Fatal(err)
	}

	// The metadata (RFC 9728), at the path the resource gives it.
	meta := newBrowser(t).get(e.srv.URL + "/.well-known/oauth-protected-resource/mcp")
	var m struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		Scopes               []string `json:"scopes_supported"`
		Bearer               []string `json:"bearer_methods_supported"`
	}
	meta.json(t, &m)
	if meta.status != 200 || m.Resource != resource || !slices.Equal(m.AuthorizationServers, []string{e.idp.URL}) ||
		!slices.Equal(m.Scopes, []string{"launchway:mcp"}) || !slices.Equal(m.Bearer, []string{"header"}) {
		t.Fatalf("metadata: %d %s", meta.status, meta.body)
	}
	listReq, hdr := modern("tools/list", "")
	r := rawPost(t, e.mcpURL(), listReq, hdr...)
	if want := `resource_metadata="https://launchway.example.com/.well-known/oauth-protected-resource/mcp"`; r.status != 401 ||
		!strings.Contains(r.header.Get("WWW-Authenticate"), want) || !strings.Contains(r.header.Get("WWW-Authenticate"), `scope="launchway:mcp"`) {
		t.Fatalf("the challenge: %d %v", r.status, r.header)
	}

	// A token for this resource, with the scope, acts as its person.
	good := e.idp.JWTAccessToken("otto", []string{"pay-ops"}, resource, "openid launchway:mcp", 10*time.Minute, nil)
	cs := mcpSession(t, e.mcpURL(), good, nil, nil)
	if out := mustCall(t, cs, "list_projects", nil); !strings.Contains(asJSON(out), `"name":"pay-api"`) || strings.Contains(asJSON(out), `"name":"web"`) {
		t.Fatalf("otto's projects: %s", asJSON(out))
	}
	started := mustCall(t, cs, "start_run", map[string]any{"project": "pay-api"})
	if entries, _ := e.eng.AuditLog(engine.AuditFilter{Action: "run.start", Limit: 5}); len(entries) != 1 || entries[0].User != "otto" ||
		!strings.HasPrefix(entries[0].Detail, "via MCP, access token of OAuth client some-agent, tool start_run") {
		t.Fatalf("audit: %+v (%s)", entries, asJSON(started))
	}
	if _, errText := call(t, cs, "start_run", map[string]any{"project": "web"}); outcome(errText) != mNotFound {
		t.Fatalf("otto starting a web run: %s", errText)
	}

	// Tokens that are not for this resource, lack the scope, have expired or
	// are ID tokens are refused; so is any such token on the rest of the API.
	for name, tok := range map[string]string{
		"another audience": e.idp.JWTAccessToken("otto", []string{"pay-ops"}, "https://elsewhere.example.com/mcp", "launchway:mcp", time.Hour, nil),
		"no scope":         e.idp.JWTAccessToken("otto", []string{"pay-ops"}, resource, "openid", time.Hour, nil),
		"expired":          e.idp.JWTAccessToken("otto", []string{"pay-ops"}, resource, "launchway:mcp", -time.Minute, nil),
		"an ID token":      e.idp.JWTAccessToken("otto", []string{"pay-ops"}, e.idp.ClientID, "launchway:mcp", time.Hour, map[string]any{"nonce": "n"}),
		"an ID token with the resource": e.idp.JWTAccessToken("otto", []string{"pay-ops"}, resource, "launchway:mcp", time.Hour,
			map[string]any{"nonce": "n"}),
		"not a JWT": "not-a-token",
	} {
		if r := rawPost(t, e.mcpURL(), listReq, append(slices.Clone(hdr), bearer(tok)...)...); r.status != http.StatusUnauthorized ||
			!strings.Contains(r.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	if code, _ := e.me(t, newBrowser(t), bearer(good)...); code != http.StatusUnauthorized {
		t.Errorf("the provider's token on the API: %d", code)
	}

	// Without groups in the token, the person's groups of their latest
	// sign-in apply; someone who never signed in has only user bindings.
	e.signIn(t, "vic")
	vic := mcpSession(t, e.mcpURL(), e.idp.JWTAccessToken("vic", nil, resource, "launchway:mcp", time.Hour, map[string]any{"groups": nil}), nil, nil)
	if out := mustCall(t, vic, "list_projects", nil); !strings.Contains(asJSON(out), `"name":"pay-api"`) {
		t.Fatalf("vic's projects: %s", asJSON(out))
	}
	stranger := mcpSession(t, e.mcpURL(), e.idp.JWTAccessToken("stranger", nil, resource, "launchway:mcp", time.Hour, map[string]any{"groups": nil}), nil, nil)
	if out := mustCall(t, stranger, "list_projects", nil); len(list(out, "projects")) != 0 {
		t.Fatalf("a stranger's projects: %s", asJSON(out))
	}

	// A Launchway token works as before.
	if out := mustCall(t, mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "plain"), nil, nil), "list_projects", nil); len(list(out, "projects")) != 2 {
		t.Fatalf("ada's projects: %s", asJSON(out))
	}
}
