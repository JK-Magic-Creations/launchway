package api

// Triggers on another project's result. The downstream project's operators
// make a trigger; the upstream project's operators allow the downstream
// project to have one (and say whether it may read the step outputs of its
// runs). Neither alone is enough, and no secret crosses.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/auth"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/source"
)

func (s *Server) runTriggerRoutes() []route {
	return []route{
		{"GET /api/v1/projects/{name}/run-triggers", projectRead, "", s.listRunTriggers},
		{"POST /api/v1/projects/{name}/run-triggers", projectOperate, "run_trigger.create", s.createRunTrigger},
		{"DELETE /api/v1/projects/{name}/run-triggers/{trigger}", projectOperate, "run_trigger.delete", s.deleteRunTrigger},
		{"GET /api/v1/projects/{name}/run-trigger-allows", projectRead, "", s.listRunTriggerAllows},
		{"POST /api/v1/projects/{name}/run-trigger-allows", projectOperate, "run_trigger.allow", s.allowRunTriggers},
		{"DELETE /api/v1/projects/{name}/run-trigger-allows/{downstream}", projectOperate, "run_trigger.revoke", s.revokeRunTriggers},
	}
}

func (s *Server) listRunTriggers(w http.ResponseWriter, r *http.Request) {
	list, err := s.Engine.RunTriggers(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createRunTrigger(w http.ResponseWriter, r *http.Request) {
	var in engine.RunTrigger
	if !decode(w, r, &in) {
		return
	}
	in.Project, in.CreatedBy = r.PathValue("name"), identityFrom(r).User
	setAudit(r, in.Name, "")
	if err := source.CheckRunTriggerExpressions(in); err != nil {
		s.fail(w, err)
		return
	}
	// The caller must be able to read the upstream project too: a trigger is
	// not a way to learn that a project exists.
	if in.UpstreamProject != "" && !s.authorize(identityFrom(r), auth.Read, in.UpstreamProject) {
		s.fail(w, fmt.Errorf("%w: project %s", engine.ErrNotFound, in.UpstreamProject))
		return
	}
	t, err := s.Engine.CreateRunTrigger(in)
	if err != nil {
		s.fail(w, err)
		return
	}
	setAudit(r, t.Name, fmt.Sprintf("%s of %s triggers on %s results of %s", t.Project, t.Name, t.Kind, t.UpstreamProject))
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) deleteRunTrigger(w http.ResponseWriter, r *http.Request) {
	setAudit(r, r.PathValue("trigger"), "")
	if err := s.Engine.DeleteRunTrigger(r.PathValue("name"), r.PathValue("trigger")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listRunTriggerAllows(w http.ResponseWriter, r *http.Request) {
	list, err := s.Engine.RunTriggerAllows(r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) allowRunTriggers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Downstream   string `json:"downstream_project"`
		ShareOutputs bool   `json:"share_outputs"`
	}
	if !decode(w, r, &in) {
		return
	}
	setAudit(r, in.Downstream, "")
	known, err := s.knownProjects()
	if err != nil {
		s.fail(w, err)
		return
	}
	found := false
	for _, p := range known {
		found = found || p == in.Downstream
	}
	if !found {
		s.fail(w, fmt.Errorf("%w: project %s", engine.ErrNotFound, in.Downstream))
		return
	}
	a := engine.RunTriggerAllow{Upstream: r.PathValue("name"), Downstream: in.Downstream, ShareOutputs: in.ShareOutputs, CreatedBy: identityFrom(r).User}
	if err := s.Engine.AllowRunTriggers(a); err != nil {
		s.fail(w, err)
		return
	}
	detail := fmt.Sprintf("%s may trigger on the results of %s", a.Downstream, a.Upstream)
	if a.ShareOutputs {
		detail += ", and read their step outputs"
	}
	setAudit(r, a.Downstream, detail)
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) revokeRunTriggers(w http.ResponseWriter, r *http.Request) {
	setAudit(r, r.PathValue("downstream"), "")
	if err := s.Engine.RevokeRunTriggers(r.PathValue("name"), r.PathValue("downstream")); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- MCP ----

type runTriggersIn struct {
	Project string `json:"project" jsonschema:"the project's name (for triggers, the downstream project that starts a run; for allows, the upstream project)"`
}

type runTriggerBrief struct {
	Name     string            `json:"name"`
	Kind     string            `json:"kind" jsonschema:"run or launch"`
	Upstream string            `json:"upstream_project"`
	When     string            `json:"when"`
	Filter   string            `json:"filter,omitempty"`
	Mapping  map[string]string `json:"mapping"`
	Enabled  bool              `json:"enabled"`
}

type runTriggersOut struct {
	Triggers []runTriggerBrief `json:"triggers"`
}

type createRunTriggerIn struct {
	Project  string            `json:"project" jsonschema:"the downstream project's name"`
	Name     string            `json:"name" jsonschema:"a name for the trigger: lower-case letters, digits and hyphens"`
	Kind     string            `json:"kind,omitempty" jsonschema:"run (default) or launch: a deployment that ended"`
	Upstream string            `json:"upstream_project" jsonschema:"the project whose results to wait for; it must have allowed this project"`
	When     string            `json:"when,omitempty" jsonschema:"run: succeeded (default), failed, canceled or any; launch: deployed (default), failed, rolled_back or any"`
	Pipeline string            `json:"upstream_pipeline,omitempty" jsonschema:"only results of the upstream pipeline with this name"`
	Filter   string            `json:"filter,omitempty" jsonschema:"an expression over run, launch and outputs that must be true"`
	Ref      string            `json:"ref,omitempty" jsonschema:"the branch or tag whose pipeline runs (default branch if empty)"`
	Mapping  map[string]string `json:"mapping,omitempty" jsonschema:"input name to an expression over run, launch and outputs"`
}

type runTriggerRef struct {
	Project string `json:"project" jsonschema:"the downstream project's name"`
	Name    string `json:"name" jsonschema:"the trigger's name"`
}

type allowIn struct {
	Project      string `json:"project" jsonschema:"the upstream project's name (whose results are triggered on)"`
	Downstream   string `json:"downstream_project" jsonschema:"the project that may trigger on it"`
	ShareOutputs bool   `json:"share_outputs,omitempty" jsonschema:"also let it read the step outputs of the upstream's runs (never secrets, logs or files)"`
}

func addRunTriggerTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_run_trigger_allows", Annotations: readOnly("List who may trigger on a project"),
		Description: "The projects an upstream project has allowed to start runs when its Sorties or launches end, and whether each may read the step outputs."},
		mcpCallTimeout, listRunTriggerAllowsTool)
	addTool(srv, c, &mcp.Tool{Name: "list_run_triggers", Annotations: readOnly("List result triggers"),
		Description: "A project's triggers on other projects' results: when the upstream project's run or launch ends the way each says, a run of this project starts with inputs mapped from the result."},
		mcpCallTimeout, listRunTriggers)
	if !c.mayOperate {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "create_run_trigger", Annotations: changes("Make a result trigger", false, false),
		Description: "Make a project start a run when another project's run or launch ends. The upstream project must have allowed this project first (allow_run_triggers), and a cycle is refused."},
		mcpCallTimeout, createRunTrigger)
	addTool(srv, c, &mcp.Tool{Name: "delete_run_trigger", Annotations: changes("Delete a result trigger", true, true),
		Description: "Remove a result trigger of a project."}, mcpCallTimeout, deleteRunTrigger)
	addTool(srv, c, &mcp.Tool{Name: "allow_run_triggers", Annotations: changes("Allow a project to trigger on this one", false, true),
		Description: "As an operator of the upstream project, let another project start runs when this project's runs or launches end. Optionally let it read their step outputs. Nothing else crosses: no secret, log or file."},
		mcpCallTimeout, allowRunTriggers)
	addTool(srv, c, &mcp.Tool{Name: "revoke_run_triggers", Annotations: changes("Stop allowing a project to trigger on this one", true, true),
		Description: "As an operator of the upstream project, withdraw the permission. The downstream project's triggers stay and are refused, with the reason, until it is given again."},
		mcpCallTimeout, revokeRunTriggers)
}

type allowListOut struct {
	Allows []engine.RunTriggerAllow `json:"allows"`
}

func listRunTriggerAllowsTool(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in runTriggersIn) (allowListOut, error) {
	var list []engine.RunTriggerAllow
	if err := c.apiJSON(ctx, "list_run_trigger_allows", http.MethodGet, apiPath("/api/v1/projects/%s/run-trigger-allows", in.Project), nil, &list); err != nil {
		return allowListOut{}, err
	}
	return allowListOut{Allows: list}, nil
}

type revokeIn struct {
	Project    string `json:"project" jsonschema:"the upstream project's name"`
	Downstream string `json:"downstream_project" jsonschema:"the project that may no longer trigger on it"`
}

func revokeRunTriggers(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in revokeIn) (okOut, error) {
	if err := c.apiJSON(ctx, "revoke_run_triggers", http.MethodDelete, apiPath("/api/v1/projects/%s/run-trigger-allows/%s", in.Project, in.Downstream), nil, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: in.Downstream + " may no longer trigger on the results of " + in.Project + "."}, nil
}

func listRunTriggers(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in runTriggersIn) (runTriggersOut, error) {
	var list []engine.RunTrigger
	if err := c.apiJSON(ctx, "list_run_triggers", http.MethodGet, apiPath("/api/v1/projects/%s/run-triggers", in.Project), nil, &list); err != nil {
		return runTriggersOut{}, err
	}
	out := runTriggersOut{Triggers: []runTriggerBrief{}}
	for _, t := range list {
		out.Triggers = append(out.Triggers, runTriggerBrief{Name: t.Name, Kind: t.Kind, Upstream: t.UpstreamProject, When: t.When, Filter: t.Filter, Mapping: t.Mapping, Enabled: t.Enabled})
	}
	return out, nil
}

func createRunTrigger(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in createRunTriggerIn) (okOut, error) {
	body := engine.RunTrigger{Name: in.Name, Kind: in.Kind, UpstreamProject: in.Upstream, When: in.When, UpstreamPipeline: in.Pipeline, Filter: in.Filter, Ref: in.Ref, Mapping: in.Mapping}
	if err := c.apiJSON(ctx, "create_run_trigger", http.MethodPost, apiPath("/api/v1/projects/%s/run-triggers", in.Project), body, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: fmt.Sprintf("Made the trigger %s: %s starts a run when %s ends.", in.Name, in.Project, in.Upstream)}, nil
}

func deleteRunTrigger(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in runTriggerRef) (okOut, error) {
	if err := c.apiJSON(ctx, "delete_run_trigger", http.MethodDelete, apiPath("/api/v1/projects/%s/run-triggers/%s", in.Project, in.Name), nil, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: "Deleted the trigger " + in.Name + "."}, nil
}

func allowRunTriggers(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in allowIn) (okOut, error) {
	body := map[string]any{"downstream_project": in.Downstream, "share_outputs": in.ShareOutputs}
	if err := c.apiJSON(ctx, "allow_run_triggers", http.MethodPost, apiPath("/api/v1/projects/%s/run-trigger-allows", in.Project), body, nil); err != nil {
		return okOut{}, err
	}
	return okOut{OK: true, Summary: fmt.Sprintf("%s may now trigger on the results of %s.", in.Downstream, in.Project)}, nil
}
