package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/auth"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// rawPost sends one HTTP request to /mcp as a client would, and returns
// the response with its body read.
func rawPost(t *testing.T, url, body string, header ...string) response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(header); i += 2 {
		if header[i+1] == "" {
			req.Header.Del(header[i])
		} else {
			req.Header.Set(header[i], header[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

// modern is a request of protocol revision 2026-07-28, with its headers.
func modern(method string, params string) (string, []string) {
	if params == "" {
		params = "{}"
	}
	var p map[string]any
	json.Unmarshal([]byte(params), &p)
	p["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": p})
	return string(body), []string{"MCP-Protocol-Version", "2026-07-28", "Mcp-Method", method}
}

// rpcResult reads a JSON-RPC response from a JSON or SSE body.
func rpcResult(t *testing.T, r response) map[string]any {
	t.Helper()
	body := r.body
	if strings.HasPrefix(r.header.Get("Content-Type"), "text/event-stream") {
		var last []byte
		for _, line := range bytes.Split(body, []byte("\n")) {
			if d, ok := bytes.CutPrefix(line, []byte("data: ")); ok {
				last = d
			}
		}
		body = last
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("not JSON-RPC: %d %s", r.status, r.body)
	}
	return m
}

// /mcp refuses what the transport's security rules say it must, and what
// is malformed or too large, with a clear status and message, and never
// with a server error.
func TestMCPTransportRefusals(t *testing.T) {
	e := mcpEnv(t, envOpts{mcp: &MCPOptions{AllowedOrigins: []string{"https://tools.example.com"}}})
	tok := e.tokenFor(t, "otto", "agent")
	list, hdr := modern("tools/list", "")
	withToken := append(slices.Clone(hdr), bearer(tok)...)

	// Who is calling.
	r := rawPost(t, e.mcpURL(), list, hdr...)
	if r.status != http.StatusUnauthorized || !strings.HasPrefix(r.header.Get("WWW-Authenticate"), `Bearer realm="launchway"`) ||
		!strings.Contains(string(r.body), "API token") {
		t.Fatalf("no token: %d %v %s", r.status, r.header, r.body)
	}
	bad := "lw_" + auth.RandomString(32)
	r = rawPost(t, e.mcpURL(), list, append(slices.Clone(hdr), bearer(bad)...)...)
	if r.status != http.StatusUnauthorized || !strings.Contains(r.header.Get("WWW-Authenticate"), `error="invalid_token"`) {
		t.Fatalf("a bad token: %d %v %s", r.status, r.header, r.body)
	}
	if entries, _ := e.eng.AuditLog(engine.AuditFilter{Action: "token.use", Limit: 5}); len(entries) == 0 || entries[0].Outcome != "denied" {
		t.Fatalf("the bad token is not in the audit trail: %+v", entries)
	}
	otto := e.signIn(t, "otto")
	session := otto.cookieValue(e.srv.URL, sessionCookie)
	r = rawPost(t, e.mcpURL(), list, append(slices.Clone(hdr), "Cookie", sessionCookie+"="+session, csrfHeader, "1")...)
	if r.status != http.StatusUnauthorized {
		t.Fatalf("a session cookie: %d %s", r.status, r.body)
	}
	if r = rawPost(t, e.mcpURL(), list, withToken...); r.status != http.StatusOK || !strings.Contains(string(r.body), "list_projects") {
		t.Fatalf("a good token: %d %s", r.status, r.body)
	}

	// Where the call comes from: pages of other sites are refused, this
	// server's own and the configured ones are not.
	for origin, want := range map[string]int{"https://evil.example": 403, "null": 403, e.srv.URL: 200, "https://tools.example.com": 200,
		"http://localhost:1": 403, strings.Replace(e.srv.URL, "127.0.0.1", "localhost", 1): 200} {
		r = rawPost(t, e.mcpURL(), list, append(slices.Clone(withToken), "Origin", origin)...)
		if r.status != want {
			t.Errorf("Origin %s: %d %s", origin, r.status, r.body)
		}
	}

	// What is sent.
	for name, c := range map[string]struct {
		body   string
		header []string
		want   int
	}{
		"not JSON":        {"{not json", withToken, 400},
		"wrong type":      {list, append(slices.Clone(withToken), "Content-Type", "text/plain"), 415},
		"no Accept":       {list, append(slices.Clone(withToken), "Accept", ""), 400},
		"too large":       {`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"x":"` + strings.Repeat("a", 2<<20) + `"}}`, withToken, 413},
		"unknown version": {list, append(slices.Clone(withToken), "MCP-Protocol-Version", "1999-01-01"), 400},
	} {
		if r = rawPost(t, e.mcpURL(), c.body, c.header...); r.status != c.want {
			t.Errorf("%s: %d, want %d: %.300s", name, r.status, c.want, r.body)
		}
	}
	// A method the server does not have, and a tool it does not have, are
	// JSON-RPC errors.
	unknown, uh := modern("tasks/frobnicate", "")
	r = rawPost(t, e.mcpURL(), unknown, append(slices.Clone(uh), bearer(tok)...)...)
	if m := rpcResult(t, r); m["error"] == nil {
		t.Errorf("an unknown method: %d %s", r.status, r.body)
	}
	callBody, ch := modern("tools/call", `{"name":"drop_database","arguments":{}}`)
	r = rawPost(t, e.mcpURL(), callBody, append(append(slices.Clone(ch), bearer(tok)...), "Mcp-Name", "drop_database")...)
	if m := rpcResult(t, r); m["error"] == nil && !strings.Contains(string(r.body), `"isError":true`) {
		t.Errorf("an unknown tool: %d %s", r.status, r.body)
	}
	// Arguments that do not fit the schema are refused before the tool runs.
	callBody, _ = modern("tools/call", `{"name":"get_run","arguments":{"run_id":42}}`)
	r = rawPost(t, e.mcpURL(), callBody, append(append(slices.Clone(ch), bearer(tok)...), "Mcp-Name", "get_run")...)
	if m := rpcResult(t, r); m["error"] == nil && !strings.Contains(string(r.body), `"isError":true`) {
		t.Errorf("a wrong argument type: %d %s", r.status, r.body)
	}

	// No sessions and no GET stream.
	req, _ := http.NewRequest(http.MethodGet, e.mcpURL(), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET: %v %v", err, resp)
	}
	resp.Body.Close()
}

// A client of an earlier protocol revision (2025-06-18, with initialize)
// works too, without a session: every request stands alone.
func TestMCPLegacyClients(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	tok := e.tokenFor(t, "otto", "old agent")
	r := rawPost(t, e.mcpURL(), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"old","version":"1"}}}`,
		bearer(tok)...)
	m := rpcResult(t, r)
	if r.status != 200 || str(m, "result", "protocolVersion") != "2025-06-18" || r.header.Get("Mcp-Session-Id") != "" {
		t.Fatalf("initialize: %d %v %s", r.status, r.header, r.body)
	}
	r = rawPost(t, e.mcpURL(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`, append(bearer(tok), "MCP-Protocol-Version", "2025-06-18")...)
	if r.status != http.StatusAccepted {
		t.Fatalf("initialized: %d %s", r.status, r.body)
	}
	r = rawPost(t, e.mcpURL(), `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_projects","arguments":{}}}`,
		append(bearer(tok), "MCP-Protocol-Version", "2025-06-18")...)
	if !strings.Contains(string(r.body), "pay-api") {
		t.Fatalf("a tool call without a session: %d %s", r.status, r.body)
	}

	// The SDK's client, made to fall back to the handshake by hiding
	// server/discover, as an older server would.
	cs := mcpSession(t, e.mcpURL(), tok, nil, &headerTransport{drop: map[string]bool{"server/discover": true}})
	if v := cs.InitializeResult().ProtocolVersion; v != "2025-11-25" {
		t.Fatalf("negotiated %s", v)
	}
	if out := mustCall(t, cs, "list_projects", nil); !strings.Contains(asJSON(out), "pay-api") {
		t.Fatalf("list_projects: %s", asJSON(out))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cs.ListPrompts(ctx, nil); err != nil {
		t.Fatal(err)
	}
}
