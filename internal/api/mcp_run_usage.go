package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

type runUsageIn struct {
	RunID string `json:"run_id" jsonschema:"the Sortie's ID (run_…)"`
	Step  string `json:"step,omitempty" jsonschema:"only this step"`
}

type runUsageOut struct {
	Steps []engine.StepUsageRecord `json:"steps" jsonschema:"each step with its attempts: measured, memory_peak_bytes, cpu_peak_milli, reserved_memory_bytes, sizing (declared, learned, default or raised), sizing_runs and killed_for_memory"`
}

func getRunUsage(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in runUsageIn) (runUsageOut, error) {
	if in.RunID == "" {
		return runUsageOut{}, errors.New("say which run: run_id")
	}
	q := ""
	if in.Step != "" {
		q = "?" + url.Values{"step": {in.Step}}.Encode()
	}
	var out runUsageOut
	if err := c.apiJSON(ctx, "get_run_usage", http.MethodGet, apiPath("/api/v1/runs/%s/usage", in.RunID)+q, nil, &out); err != nil {
		return runUsageOut{}, err
	}
	if out.Steps == nil {
		out.Steps = []engine.StepUsageRecord{}
	}
	return out, nil
}

func addRunUsageTool(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_run_usage", Annotations: readOnly("Get what a Sortie's steps used"),
		Description: "What each step of a Sortie used (peak memory, peak CPU, CPU time) and was given, attempt by attempt, with where its numbers came from: declared in the pipeline file, learned from earlier Sorties, the installation's default, or raised after a kill for memory. Optionally one step."},
		mcpCallTimeout, getRunUsage)
}
