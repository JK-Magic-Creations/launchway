package main

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/jk-magic-creations/launchway/internal/testlib"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain lets a test run this binary as launchway mcp, the way an agent
// starts it.
func TestMain(m *testing.M) {
	testlib.NoPrompt()
	if os.Getenv("LAUNCHWAY_TEST_RUN_MCP") == "1" {
		if err := cmdMCP(nil); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// mcpCommand is launchway mcp as a subprocess, talking to a server.
func mcpCommand(t *testing.T, server, token string) *exec.Cmd {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "LAUNCHWAY_TEST_RUN_MCP=1", "LAUNCHWAY_SERVER="+server, "LAUNCHWAY_TOKEN="+token,
		"LAUNCHWAY_CONFIG_DIR="+t.TempDir())
	cmd.Stderr = os.Stderr
	return cmd
}

// An agent that starts MCP servers as commands runs launchway mcp, and gets
// the server's tools with the token's rights, through a real MCP client
// over standard input and output.
func TestMCPOverStdio(t *testing.T) {
	c, _ := cliServer(t)
	var mu sync.Mutex
	var progress []string
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-agent", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			progress = append(progress, r.Params.Message)
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: mcpCommand(t, c.Server, c.Token)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if v := cs.InitializeResult().ProtocolVersion; v != "2026-07-28" {
		t.Fatalf("negotiated %s", v)
	}
	var names []string
	for tool, err := range cs.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	if !strings.Contains(strings.Join(names, ","), "start_run") {
		t.Fatalf("tools: %v", names)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "start_run", Arguments: map[string]any{"project": "app", "inputs": map[string]any{"outcome": "fail"}}})
	if err != nil || res.IsError {
		t.Fatalf("start_run: %v %+v", err, res)
	}
	var started struct {
		RunID string `json:"run_id"`
	}
	data, _ := json.Marshal(res.StructuredContent)
	json.Unmarshal(data, &started)
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "wait_for_run", Arguments: map[string]any{"run_id": started.RunID, "timeout_seconds": 30},
		Meta: mcp.Meta{"progressToken": 1}})
	if err != nil || res.IsError || !strings.Contains(jsonOf(res.StructuredContent), `"status":"failed"`) {
		t.Fatalf("wait_for_run: %v %s", err, jsonOf(res))
	}
	mu.Lock()
	if len(progress) == 0 {
		t.Error("no progress notifications came through")
	}
	mu.Unlock()
	ref, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "launchway://docs/agent-guide"})
	if err != nil || !strings.Contains(ref.Contents[0].Text, "Diagnosing a failed run") {
		t.Fatalf("the guide: %v", err)
	}
	// A refusal from the server reaches the agent as such.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_run", Arguments: map[string]any{"run_id": "run_nothere"}})
	if err != nil || !res.IsError || !strings.Contains(jsonOf(res.Content), "not found") {
		t.Fatalf("a missing run: %v %s", err, jsonOf(res))
	}
}

// The command passes on a refused token as an error the agent can show, and
// survives malformed input.
func TestMCPStdioRefusalsAndBadInput(t *testing.T) {
	c, _ := cliServer(t)
	cmd := mcpCommand(t, c.Server, "lw_wrong")
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Wait()
	rd := bufio.NewReader(out)
	send := func(line string) map[string]any {
		t.Helper()
		io.WriteString(in, line+"\n")
		got, err := rd.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(got), &m); err != nil {
			t.Fatalf("not JSON: %q", got)
		}
		return m
	}
	if m := send("{not json"); !strings.Contains(jsonOf(m["error"]), "not a JSON-RPC message") {
		t.Fatalf("bad input: %v", m)
	}
	m := send(`{"jsonrpc":"2.0","id":5,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if m["id"] != float64(5) || !strings.Contains(jsonOf(m["error"]), "token was refused") {
		t.Fatalf("a refused token: %v", m)
	}
	in.Close()
}

func jsonOf(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
