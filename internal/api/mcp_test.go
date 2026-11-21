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
		mutating := slices.Contains([]string{"start_run", "add_graph_override", "remove_graph_override", "propose_requirements_file", "refresh_graph", "rescan_graph", "rebuild_graph", "advance_release_formation", "attach_release_assets", "cancel_release_freeze", "create_release", "create_release_formation", "create_release_freeze", "delete_draft_release", "edit_release", "give_release_clearance", "make_release_candidate", "plan_release_promotion", "promote_release", "promote_release_formation", "propose_release_changelog", "publish_release", "remove_release_asset", "return_to_base", "set_release_checklist", "set_release_installation", "set_release_settings", "upload_release_file", "withdraw_release", "accept_finding", "reopen_finding", "queue_pull_request", "update_pull_branch", "mark_pull_files_viewed", "create_run_trigger", "delete_run_trigger", "allow_run_triggers", "revoke_run_triggers", "replay_event", "delete_event_source", "cancel_run", "queue_run", "trust_run", "retry_run", "decide_approval", "control_rollout", "rollback_deployment", "review_rollback", "mark_verification", "set_step_resource_limits", "set_step_resource_default", "set_mirror", "sync_mirror", "stop_mirroring", "add_connection", "import_repositories", "change_mirror", "remove_connection", "delete_cache", "set_cache_policy", "update_repository", "delete_repository", "create_repository", "set_variable", "delete_variable", "lock_variable", "unlock_variable", "add_ssh_key", "delete_ssh_key", "add_deploy_key", "delete_deploy_key", "set_protected_branches", "import_archive", "write_repository_file", "create_repository_branch", "delete_repository_branch", "delete_repository_file", "add_webhook", "update_webhook", "delete_webhook", "test_webhook", "redeliver_webhook", "create_pull_request", "update_pull_request", "merge_pull_request", "revert_pull_request", "comment_on_pull_request", "update_pull_comment", "review_pull_request", "set_pull_settings", "delete_registry_image", "set_registry_policy", "run_registry_retention", "registry_gc", "create_toggle", "set_toggle_state", "revert_toggle_change", "approve_toggle_change", "reject_toggle_change", "withdraw_toggle_change", "revert_toggle_changes_since", "update_toggle", "delete_toggle", "save_toggle_segment", "apply_toggles_file", "revoke_toggle_key", "cancel_toggle_schedule", "lock_toggle", "unlock_toggle", "delete_toggle_segment", "set_toggle_safeguards"}, tool.Name)
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
