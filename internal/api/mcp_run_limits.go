package api

// The MCP tools for the installation's limits on pipelines (schema 24):
// reading them, and, for an administrator, changing them. Each calls the
// same routes as the API, so the answer for a caller is the API's, and
// changes are audited.

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func addRunLimitTools(srv *mcp.Server, c *mcpCall) {
	addImportPipelineTool(srv, c) // the pipeline importer (mcp_import_pipeline.go)
	addEncryptionTools(srv, c)    // administrators' report on encryption at rest (mcp_encryption.go)
	addTool(srv, c, &mcp.Tool{Name: "get_run_limits", Annotations: readOnly("Get run limits"),
		Description: "The installation's limits on pipelines: max_run_timeout, the longest timeout a pipeline may ask for with its top-level timeout. An empty value is not set."},
		mcpCallTimeout, getRunLimits)
	if !c.mayAdminister {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "set_run_limits", Annotations: changes("Set run limits", true, true),
		Description: "Administrators only. Replace the installation's limits on pipelines: max_run_timeout, such as 48h (1m to 720h); empty for none. A pipeline whose timeout is longer is refused when a Sortie starts and by validate_pipeline. Audited."},
		mcpCallTimeout, setRunLimits)
}

type getRunLimitsIn struct{}

func getRunLimits(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ getRunLimitsIn) (engine.RunLimits, error) {
	var out engine.RunLimits
	err := c.apiJSON(ctx, "get_run_limits", http.MethodGet, "/api/v1/run-limits", nil, &out)
	return out, err
}

type setRunLimitsIn struct {
	MaxRunTimeout string `json:"max_run_timeout,omitempty" jsonschema:"the longest timeout a pipeline may ask for, such as 48h; empty for none"`
}

func setRunLimits(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setRunLimitsIn) (engine.RunLimits, error) {
	var out engine.RunLimits
	err := c.apiJSON(ctx, "set_run_limits", http.MethodPut, "/api/v1/run-limits", in, &out)
	return out, err
}
