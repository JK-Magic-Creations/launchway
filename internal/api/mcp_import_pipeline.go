package api

// The MCP tool that proposes a Launchway pipeline from the pipeline files of
// another system. It calls the same route as the API, so the answer is the API's.

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addImportPipelineTool(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "import_pipeline", Annotations: readOnly("Import a pipeline"),
		Description: "Turn the pipeline files of another system (hosted workflow files, a stage-based pipeline file, a declarative Groovy pipeline file, a jobs-and-workflows config, a tasks-and-stages file) into a proposed .launchway/pipeline.yaml. Send the files with their repository paths. Returns the proposal and a finding for every construct: converted exactly, converted with a stated difference, or not converted with the reason and what to do. Nothing is stored or run; check the proposal with validate_pipeline and commit it yourself. With dry true, steps that push, release or deploy are switched off behind the input enable_delivery, so results can be compared with the old system first."},
		mcpCallTimeout, importPipeline)
}

func importPipeline(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in ImportRequest) (ImportResponse, error) {
	var out ImportResponse
	err := c.apiJSON(ctx, "import_pipeline", http.MethodPost, "/api/v1/import/pipeline", in, &out)
	return out, err
}
