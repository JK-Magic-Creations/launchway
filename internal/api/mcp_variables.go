package api

// The MCP tools for variables at levels (schema 29): what is in effect for a
// project or at a level, with the chain behind each variable, and, for those
// who may, setting, deleting, locking and unlocking. Each calls the same
// routes as the API, so the answer for a caller is the API's: the same
// permissions by level, the same refusals, the same audit.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/settings"
)

func addVariableTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_effective_variables", Annotations: readOnly("Get a project's variables in effect"),
		Description: "Every variable in effect for a project, optionally for one Airfield, each with its chain: the winning value, the level it comes from, every value it overrides in order, whether it is locked and by whom, and why. A secret shows its reference, never a value. " + settings.VariableRule},
		mcpCallTimeout, getEffectiveVariables)
	addTool(srv, c, &mcp.Tool{Name: "list_variables", Annotations: readOnly("List variables at a level"),
		Description: "The variables at a level (installation, organization, unit or project; scope names the Squadron or the project) with what each inherits from above, and whether this level may override it. " + settings.VariableRule},
		mcpCallTimeout, listVariables)
	addTool(srv, c, &mcp.Tool{Name: "get_run_variables", Annotations: readOnly("Get the variables a Sortie used"),
		Description: "The variables a Sortie used, as recorded when it was created, each with its chain at that moment; a re-run shows the original's. A secret shows its reference."},
		mcpCallTimeout, getRunVariables)
	if !c.mayOperate {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "set_variable", Annotations: changes("Set a variable", true, true),
		Description: "Set a variable at a level (scope names the Squadron or the project; Airfield makes it apply to one Airfield only). Types: string, number, boolean, list, map, secret (a reference { store, key, field }, never the secret) and connection (the name of a connection; of says what kind). A name keeps one type everywhere. Refused when a lock above forbids it. Who may: an operator of the project, or an administrator of the unit, the organization or the installation. Audited."},
		mcpCallTimeout, setVariable)
	addTool(srv, c, &mcp.Tool{Name: "delete_variable", Annotations: changes("Delete a variable", true, true),
		Description: "Delete a variable set at a level. What it overrode applies again. Audited."},
		mcpCallTimeout, deleteVariable)
	addTool(srv, c, &mcp.Tool{Name: "lock_variable", Annotations: changes("Lock a variable", true, true),
		Description: "Lock a variable at a level, with a reason: nobody below may change it, a pipeline file or a Sortie included. Settings below it are kept but ignored, and listed in the answer. Needs an administrator of that level. Audited."},
		mcpCallTimeout, lockVariable)
	addTool(srv, c, &mcp.Tool{Name: "unlock_variable", Annotations: changes("Unlock a variable", true, true),
		Description: "Remove the lock of a variable at a level; settings below it count again. Needs an administrator of that level. Audited."},
		mcpCallTimeout, unlockVariable)
}

type effectiveVariablesIn struct {
	Project     string `json:"project" jsonschema:"the project's name, as list_projects gives it"`
	Environment string `json:"environment,omitempty" jsonschema:"the environment being deployed to; without one, only the general variables"`
}

func getEffectiveVariables(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in effectiveVariablesIn) (map[string]any, error) {
	if in.Project == "" {
		return nil, errors.New("say which project")
	}
	var out map[string]any
	path := apiPath("/api/v1/projects/%s/variables", in.Project)
	if in.Environment != "" {
		path += "?environment=" + url.QueryEscape(in.Environment)
	}
	err := c.apiJSON(ctx, "get_effective_variables", http.MethodGet, path, nil, &out)
	return out, err
}

type levelIn struct {
	Level       string `json:"level" jsonschema:"installation, organization, unit or project"`
	Scope       string `json:"scope,omitempty" jsonschema:"the Squadron's or the project's name; none for the Base and the Wing"`
	Environment string `json:"environment,omitempty" jsonschema:"resolve for this environment"`
}

func listVariables(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in levelIn) (map[string]any, error) {
	var out map[string]any
	q := url.Values{"level": {in.Level}, "scope": {in.Scope}, "environment": {in.Environment}}
	err := c.apiJSON(ctx, "list_variables", http.MethodGet, "/api/v1/variables?"+q.Encode(), nil, &out)
	return out, err
}

type setVariableIn struct {
	Name        string `json:"name" jsonschema:"the variable's name: lower-case letters, digits and _, starting with a letter"`
	Level       string `json:"level" jsonschema:"installation, organization, unit or project"`
	Scope       string `json:"scope,omitempty" jsonschema:"the Squadron's or the project's name"`
	Environment string `json:"environment,omitempty" jsonschema:"apply only when deploying to this environment"`
	Type        string `json:"type" jsonschema:"string, number, boolean, list, map, secret or connection"`
	Of          string `json:"of,omitempty" jsonschema:"for a connection, what kind of outside system it is (registry, cluster, ...)"`
	Value       any    `json:"value" jsonschema:"the value in its type: text, a number, true or false, a list, a map, a secret reference { store, key, field } or a connection's name"`
	Description string `json:"description,omitempty" jsonschema:"one line saying what it is for"`
}

func setVariable(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setVariableIn) (map[string]any, error) {
	if in.Name == "" {
		return nil, errors.New("give the variable's name")
	}
	raw, err := json.Marshal(in.Value)
	if err != nil {
		return nil, err
	}
	body := variableBody{Level: in.Level, Scope: in.Scope, Environment: in.Environment, Type: in.Type, Of: in.Of, Value: raw, Description: in.Description}
	var out map[string]any
	err = c.apiJSON(ctx, "set_variable", http.MethodPut, apiPath("/api/v1/variables/%s", in.Name), body, &out)
	return out, err
}

type variableKeyIn struct {
	Name        string `json:"name" jsonschema:"the variable's name"`
	Level       string `json:"level" jsonschema:"installation, organization, unit or project"`
	Scope       string `json:"scope,omitempty" jsonschema:"the Squadron's or the project's name"`
	Environment string `json:"environment,omitempty" jsonschema:"the environment the variable is for, if it is for one"`
}

func (k variableKeyIn) query() string {
	return "?" + url.Values{"level": {k.Level}, "scope": {k.Scope}, "environment": {k.Environment}}.Encode()
}

func deleteVariable(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in variableKeyIn) (map[string]bool, error) {
	var out map[string]bool
	err := c.apiJSON(ctx, "delete_variable", http.MethodDelete, apiPath("/api/v1/variables/%s", in.Name)+in.query(), nil, &out)
	return out, err
}

type lockVariableIn struct {
	variableKeyIn
	Reason string `json:"reason" jsonschema:"one line saying why; it is shown wherever the lock stops someone"`
}

func lockVariable(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in lockVariableIn) (map[string]any, error) {
	var out map[string]any
	body := variableBody{Level: in.Level, Scope: in.Scope, Environment: in.Environment, Reason: in.Reason}
	err := c.apiJSON(ctx, "lock_variable", http.MethodPut, apiPath("/api/v1/variables/%s/lock", in.Name), body, &out)
	return out, err
}

func unlockVariable(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in variableKeyIn) (map[string]any, error) {
	var out map[string]any
	err := c.apiJSON(ctx, "unlock_variable", http.MethodDelete, apiPath("/api/v1/variables/%s/lock", in.Name)+in.query(), nil, &out)
	return out, err
}

func getRunVariables(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
	Run string `json:"run" jsonschema:"the run's id"`
}) (map[string]any, error) {
	var out map[string]any
	err := c.apiJSON(ctx, "get_run_variables", http.MethodGet, apiPath("/api/v1/runs/%s/variables", in.Run), nil, &out)
	return out, err
}
