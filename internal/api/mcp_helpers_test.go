package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/runner"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// headerTransport adds headers to every request, and may send each one to
// the next of several servers in turn, as a load balancer would.
type headerTransport struct {
	header  http.Header
	mu      sync.Mutex
	targets []string // host:port of each server; empty: as addressed
	next    int
	hosts   []string // the hosts requests went to, in order
	// drop, if set, answers 404 for requests whose Mcp-Method it names.
	drop map[string]bool
}

func (h *headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.header {
		r.Header[k] = v
	}
	if h.drop[r.Header.Get("Mcp-Method")] {
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: http.Header{},
			Body: http.NoBody, Request: r}, nil
	}
	h.mu.Lock()
	if len(h.targets) > 0 {
		r.URL.Host = h.targets[h.next%len(h.targets)]
		r.Host = r.URL.Host
		h.next++
	}
	h.hosts = append(h.hosts, r.URL.Host)
	h.mu.Unlock()
	return http.DefaultTransport.RoundTrip(r)
}

// mcpSession connects a real MCP client to endpoint with a token.
func mcpSession(t *testing.T, endpoint, token string, opts *mcp.ClientOptions, tr *headerTransport) *mcp.ClientSession {
	t.Helper()
	if tr == nil {
		tr = &headerTransport{}
	}
	if tr.header == nil {
		tr.header = http.Header{}
	}
	if token != "" {
		tr.header.Set("Authorization", "Bearer "+token)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "launchway-test-agent", Version: "1"}, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Transport: tr},
		DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatalf("connecting to %s: %v", endpoint, err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// call calls a tool and returns its structured result, decoded into a map,
// and its error text if it failed.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (map[string]any, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, "protocol error: " + err.Error()
	}
	if res.IsError {
		var b strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return nil, b.String()
	}
	out := map[string]any{}
	data, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("%s: structured content %s: %v", tool, data, err)
	}
	return out, ""
}

// mustCall is call for a tool that must succeed.
func mustCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	out, errText := call(t, cs, tool, args)
	if errText != "" {
		t.Fatalf("%s %v: %s", tool, args, errText)
	}
	return out
}

// toolNames lists the tools a session sees.
func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	var names []string
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	return names
}

// mcpEnv is a server with sign-in and the MCP endpoint on.
func mcpEnv(t *testing.T, o envOpts) *authEnv {
	t.Helper()
	if o.mcp == nil {
		o.mcp = &MCPOptions{}
	}
	return setupAuth(t, o)
}

func (e *authEnv) mcpURL() string { return e.srv.URL + mcpPath }

// tokenFor makes an API token for a test person.
func (e *authEnv) tokenFor(t *testing.T, user, name string) string {
	t.Helper()
	_, tok := e.signIn(t, user).newToken(e.srv.URL, name)
	return tok
}

// hostRunner runs steps on this machine for the server, with the given
// secret stores.
func (e *authEnv) hostRunner(t *testing.T, name string, stores *secrets.Resolver) {
	t.Helper()
	if stores == nil {
		stores = secrets.NewResolver()
	}
	r := &runner.Runner{Client: runner.NewClient(e.srv.URL, "runner-secret"), Exec: &executor.Host{},
		Info: engine.RunnerInfo{ID: name, Name: name}, DataDir: filepath.Join(e.dir, name), Concurrency: 2, Log: quiet(), Secrets: stores}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

// str digs a string out of a decoded result: str(m, "run", "id").
func str(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[k]
	}
	s, _ := v.(string)
	return s
}

func list(m map[string]any, key string) []any {
	l, _ := m[key].([]any)
	return l
}

func asJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
