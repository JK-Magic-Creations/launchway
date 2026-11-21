package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP server's quality bar (docs/development/interface-quality.md): every tool says in a sentence what it does
// and when to use it, describes each input field, tells a client whether it only reads or changes things (and
// whether the change is destructive), returns structured results, and fails with a message an assistant can act
// on. This test checks all of them, so a new tool cannot arrive without them.
func TestMCPToolsMeetTheQualityBar(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	cs := mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "all"), nil, nil)
	n := 0
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		name := tool.Name
		if len(strings.Fields(tool.Description)) < 6 || !strings.HasSuffix(strings.TrimSpace(tool.Description), ".") {
			t.Errorf("%s: the description %q should be a sentence of at least six words, ending with a full stop", name, tool.Description)
		}
		a := tool.Annotations
		if a == nil || strings.TrimSpace(a.Title) == "" {
			t.Errorf("%s: annotations with a title are needed", name)
			continue
		}
		if !a.ReadOnlyHint {
			if a.DestructiveHint == nil {
				t.Errorf("%s: a tool that changes things must say whether it is destructive", name)
			}
			if a.OpenWorldHint == nil {
				t.Errorf("%s: a tool that changes things must say whether it reaches outside Launchway", name)
			}
		}
		if a.ReadOnlyHint && a.DestructiveHint != nil && *a.DestructiveHint {
			t.Errorf("%s: a read-only tool cannot be destructive", name)
		}
		schema := toMap(t, tool.InputSchema)
		if schema["type"] != "object" {
			t.Errorf("%s: the input schema is %v, not an object", name, schema["type"])
		}
		props, _ := schema["properties"].(map[string]any)
		for field, def := range props {
			if d, _ := def.(map[string]any)["description"].(string); len(strings.Fields(d)) < 2 {
				t.Errorf("%s: the input field %q has no description an assistant can read (%q)", name, field, d)
			}
		}
		for _, r := range asStrings(schema["required"]) {
			if _, ok := props[r]; !ok {
				t.Errorf("%s: required field %q is not a property", name, r)
			}
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s: no output schema: results must be structured", name)
		}
	}
	if n < 40 {
		t.Errorf("only %d tools listed", n)
	}
}

// A call that fails says what failed in plain words, as a tool error (the assistant can read it and try again),
// not as a protocol error and not as a bare status code.
func TestMCPErrorsAreSentencesAnAssistantCanActOn(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	cs := mcpSession(t, e.mcpURL(), e.tokenFor(t, "ada", "all"), nil, nil)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_run", Arguments: map[string]any{"run_id": "run_does_not_exist"}})
	if err != nil {
		t.Fatalf("a missing run came back as a protocol error: %v", err)
	}
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("a missing run is a tool error with a message: %+v", res)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if len(strings.Fields(text)) < 3 || strings.Contains(text, "HTTP ") || strings.HasPrefix(text, "{") {
		t.Errorf("the message %q is not plain words", text)
	}
}

func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func asStrings(v any) []string {
	var out []string
	if l, ok := v.([]any); ok {
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
