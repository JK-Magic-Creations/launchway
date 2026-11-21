package api

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A real MCP client connects with a token, negotiates the current protocol
// revision, and finds the server's instructions, tools, resources and
// prompts.
func TestMCPConnectsAndDescribesItself(t *testing.T) {
	e := mcpEnv(t, envOpts{})
	cs := mcpSession(t, e.mcpURL(), e.tokenFor(t, "otto", "agent"), nil, nil)
	init := cs.InitializeResult()
	if init == nil || init.ProtocolVersion != "2026-07-28" || init.ServerInfo.Name != "launchway" ||
		!strings.Contains(init.Instructions, "untrusted") {
		t.Fatalf("initialize: %+v", init)
	}
	names := toolNames(t, cs)
	for _, want := range []string{"list_projects", "get_run", "validate_pipeline", "start_run", "wait_for_run", "decide_approval"} {
		if !slices.Contains(names, want) {
			t.Errorf("no tool %s in %v", want, names)
		}
	}
	for tool, err := range cs.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Description == "" || tool.Annotations == nil || tool.InputSchema == nil {
			t.Errorf("%s: description, annotations and input schema are needed: %+v", tool.Name, tool)
		}
		mutating := slices.Contains([]string{"start_run", "cancel_run", "queue_run", "trust_run", "retry_run", "decide_approval", "control_rollout", "rollback_deployment", "review_rollback", "mark_verification", "set_step_resource_limits", "set_step_resource_default", "set_mirror", "sync_mirror", "stop_mirroring", "add_connection", "import_repositories", "change_mirror", "remove_connection", "delete_cache", "set_cache_policy", "update_repository", "delete_repository", "create_repository", "set_variable", "delete_variable", "lock_variable", "unlock_variable", "add_ssh_key", "delete_ssh_key", "add_deploy_key", "delete_deploy_key", "set_protected_branches", "add_webhook", "update_webhook", "delete_webhook", "test_webhook", "redeliver_webhook", "create_pull_request", "update_pull_request", "merge_pull_request", "revert_pull_request", "comment_on_pull_request", "update_pull_comment", "review_pull_request", "set_pull_settings"}, tool.Name)
		if tool.Annotations.ReadOnlyHint == mutating {
			t.Errorf("%s: readOnlyHint %v", tool.Name, tool.Annotations.ReadOnlyHint)
		}
		if mutating && tool.Annotations.DestructiveHint == nil {
			t.Errorf("%s: no destructiveHint", tool.Name)
		}
	}
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "launchway://docs/pipeline-reference"})
	if err != nil || len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, "# Launchway pipeline reference") {
		t.Fatalf("reference: %v %+v", err, res)
	}
	p, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "diagnose_failed_run", Arguments: map[string]string{"run_id": "run_x"}})
	if err != nil || len(p.Messages) != 1 || !strings.Contains(p.Messages[0].Content.(*mcp.TextContent).Text, "get_step_log") {
		t.Fatalf("prompt: %v %+v", err, p)
	}
	out := mustCall(t, cs, "list_projects", nil)
	if got := asJSON(out["projects"]); !strings.Contains(got, `"name":"pay-api"`) || strings.Contains(got, `"name":"web"`) {
		t.Fatalf("otto's projects: %s", got)
	}
}
