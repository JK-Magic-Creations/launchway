package api

// The MCP tools for dependency updates: what a repository depends on, what is
// newer or vulnerable, and what the updater would open. Both call the same
// routes as the API, so a caller sees what the API lets them see. They change
// nothing: the updater itself runs as a pipeline step.

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addDependencyTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_dependencies", Annotations: readOnly("List dependencies"),
		Description: "The dependencies of a hosted repository's default branch, read from its manifests and lock files (Go modules, npm, Python, container images, workflow references): for each the ecosystem, name, version in use, newest version, the version the policy would move to, its age, known vulnerabilities with severity and the fixed version, and why nothing is proposed when that is so. Also the settings that apply and the ecosystems that are coming but not available. Reads the default branch only and changes nothing."},
		mcpCallTimeout, listDependencies)
	addTool(srv, c, &mcp.Tool{Name: "preview_updates", Annotations: readOnly("Preview dependency updates"),
		Description: "What the dependency updater would do for a hosted repository right now, ignoring the schedule: the pull requests it would open (security fixes first), update in place or close, each with the updates in it, and what is queued behind the limits with the reason. Nothing is opened; the updater runs as a pipeline step on a runner."},
		mcpCallTimeout, previewUpdates)
}

type depsIn struct {
	Repository string `json:"repository" jsonschema:"the repository, owner/name"`
}

func listDependencies(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in depsIn) (map[string]any, error) {
	return depsCall(ctx, c, "list_dependencies", in, "/dependencies")
}

func previewUpdates(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in depsIn) (map[string]any, error) {
	return depsCall(ctx, c, "preview_updates", in, "/updates/preview")
}

func depsCall(ctx context.Context, c *mcpCall, tool string, in depsIn, suffix string) (map[string]any, error) {
	out := map[string]any{}
	owner, name, err := splitRepo(in.Repository)
	if err != nil {
		return out, err
	}
	err = c.apiJSON(ctx, tool, http.MethodGet, apiPath("/api/v1/repositories/%s/%s", owner, name)+suffix, nil, &out)
	return out, err
}
