package api

// The MCP tools for step resources (schema 22): reading the installation's
// default CPU and memory per step kind and its limits, and, for an
// administrator, changing them. Each calls the same routes as the API, so
// the answer for a caller is the API's, and changes are audited.

import (
	"context"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

func addStepResourceTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_step_resources", Annotations: readOnly("Get step resources"),
		Description: "The installation's default CPU and memory for each kind of step (what a step gets when it writes no resources), and its limits: the most a step may ask for and the memory one parallel job is assumed to need. An empty value is not set."},
		mcpCallTimeout, getStepResources)
	if !c.mayAdminister {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "set_step_resource_limits", Annotations: changes("Set step resource limits", true, true),
		Description: "Administrators only. Replace the installation's step resource limits: max_cpu and max_memory (the most a step may ask for) and memory_per_thread. A value left out is not set. Refused if a default is above the new maximum. Audited."},
		mcpCallTimeout, setStepResourceLimits)
	addTool(srv, c, &mcp.Tool{Name: "set_step_resource_default", Annotations: changes("Set a step resource default", true, true),
		Description: "Administrators only. Replace the default resources of one step kind (kind step): cpu, cpu_limit, memory, memory_limit, as 500m, 2, 512Mi or 6Gi; a value left out is not set. With recommended true, set the values Launchway recommends instead (what a fresh installation starts with; an upgraded one starts with nothing sized). Applies to the next pod a run starts. Audited."},
		mcpCallTimeout, setStepResourceDefault)
}

type getStepResourcesIn struct{}

func getStepResources(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ getStepResourcesIn) (engine.StepResourcesConfig, error) {
	var out engine.StepResourcesConfig
	err := c.apiJSON(ctx, "get_step_resources", http.MethodGet, "/api/v1/step-resources", nil, &out)
	return out, err
}

type setLimitsIn struct {
	MaxCPU          string `json:"max_cpu,omitempty" jsonschema:"the most CPU a step may ask for, such as 8 or 500m"`
	MaxMemory       string `json:"max_memory,omitempty" jsonschema:"the most memory a step may ask for, such as 16Gi"`
	MemoryPerThread string `json:"memory_per_thread,omitempty" jsonschema:"memory one parallel job is assumed to need, at least 64Mi; default 1Gi"`
}

func setStepResourceLimits(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setLimitsIn) (engine.ResourceLimits, error) {
	var out engine.ResourceLimits
	err := c.apiJSON(ctx, "set_step_resource_limits", http.MethodPut, "/api/v1/step-resources/limits", in, &out)
	return out, err
}

type setDefaultIn struct {
	Kind        string `json:"kind" jsonschema:"the step kind: step"`
	CPU         string `json:"cpu,omitempty" jsonschema:"CPU promised, such as 1 or 500m"`
	CPULimit    string `json:"cpu_limit,omitempty" jsonschema:"the most CPU a step may use; not below cpu"`
	Memory      string `json:"memory,omitempty" jsonschema:"memory promised, such as 2Gi"`
	MemoryLimit string `json:"memory_limit,omitempty" jsonschema:"the most memory a step may use; not below memory"`
	Recommended bool   `json:"recommended,omitempty" jsonschema:"set Launchway's recommended values for the kind instead of the ones given"`
}

func setStepResourceDefault(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setDefaultIn) (engine.ResourceDefault, error) {
	var out engine.ResourceDefault
	if in.Kind == "" {
		return out, errors.New("say which kind: kind")
	}
	body := map[string]any{"cpu": in.CPU, "cpu_limit": in.CPULimit, "memory": in.Memory, "memory_limit": in.MemoryLimit}
	if in.Recommended {
		body["recommended"] = true
	}
	err := c.apiJSON(ctx, "set_step_resource_default", http.MethodPut, apiPath("/api/v1/step-resources/defaults/%s", in.Kind), body, &out)
	return out, err
}
