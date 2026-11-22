package api

// The MCP tools for configuration in the database (schema 23): read what an
// administrator manages (projects, libraries, environments, runner pools,
// role bindings, policies, health sources, models, secret stores, units and
// the installation's settings), change it with the same checks, version
// rules and history as the API, and use the same export and import. Each
// tool calls the same route as the HTTP API and the CLI. Everything except
// the overview needs the admin role on every project, so only an
// administrator's token lists them.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addConfigTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_config_overview", Annotations: readOnly("Get the configuration overview"),
		Description: "The kinds of configuration objects this installation keeps in its database (Flights, libraries, Airfields, Station pools, role bindings, policies, policy sources, health sources, AI models, secret stores, units, installation settings), what the levels are called here, and whether this server keeps configuration in the database."},
		mcpCallTimeout, getConfigOverview)
	if !c.mayAdminister {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "list_config", Annotations: readOnly("List configuration objects"),
		Description: "List the configuration objects of one kind, with their versions. kind is one of projects, libraries, Airfields, runner-pools, role-bindings, policies, policy-sources, health-sources, models, secret-stores, units, installation, organization. With deleted true, list the deleted ones, which can be restored."},
		mcpCallTimeout, listConfig)
	addTool(srv, c, &mcp.Tool{Name: "get_config", Annotations: readOnly("Get a configuration object"),
		Description: "One configuration object: its spec (the settings, with the keys the configuration file uses), version, who last changed it and who manages it (api, terraform, import)."},
		mcpCallTimeout, getConfig)
	addTool(srv, c, &mcp.Tool{Name: "put_config", Annotations: changes("Create or replace a configuration object", true, true),
		Description: "Administrators only. Create or replace one configuration object: the whole spec replaces the old one. The change is checked by the rules the configuration file is checked by (every problem is listed), recorded in the object's history and the audit trail, and takes effect on every server within seconds. Pass version (from get_config) to refuse the change if somebody else changed the object meanwhile; version -1 means it must not exist yet. Use validate_config first to check without changing."},
		mcpCallTimeout, putConfig)
	addTool(srv, c, &mcp.Tool{Name: "delete_config", Annotations: changes("Delete a configuration object", true, true),
		Description: "Administrators only. Delete one configuration object. It is kept and can be brought back with restore_config. Refused if something still depends on it (a pool a Flight uses, say) or if it would leave the configuration invalid."},
		mcpCallTimeout, deleteConfig)
	addTool(srv, c, &mcp.Tool{Name: "restore_config", Annotations: changes("Restore a configuration object", false, true),
		Description: "Administrators only. Bring a deleted configuration object back as it was."},
		mcpCallTimeout, restoreConfig)
	addTool(srv, c, &mcp.Tool{Name: "get_config_history", Annotations: readOnly("Get configuration history"),
		Description: "Who changed what and when: the changes of one object (kind and name), of one kind, or of everything, newest first, each with what changed. before and limit page through it."},
		mcpCallTimeout, getConfigHistory)
	addTool(srv, c, &mcp.Tool{Name: "validate_config", Annotations: readOnly("Validate configuration changes"),
		Description: "Check a list of configuration changes as apply_config would make them, storing nothing. Each change is {kind, name, action (put, delete or restore), unit, spec, version}. Returns whether they are valid and every problem."},
		mcpCallTimeout, validateConfig)
	addTool(srv, c, &mcp.Tool{Name: "apply_config", Annotations: changes("Apply several configuration changes", true, true),
		Description: "Administrators only. Make several configuration changes together, all or none (what a wizard does): each is {kind, name, action (put, delete or restore), unit, spec, version}. Validate first with validate_config."},
		mcpCallTimeout, applyConfig)
	addTool(srv, c, &mcp.Tool{Name: "get_effective_config", Annotations: readOnly("Get a Flight's effective configuration"),
		Description: "What applies to a Flight, setting by setting, and where each comes from (the Base, the Wing, a Squadron, the Flight)."},
		mcpCallTimeout, getEffectiveConfig)
	addTool(srv, c, &mcp.Tool{Name: "export_config", Annotations: readOnly("Export the configuration"),
		Description: "The whole configuration as YAML text, for backup, review or Git. import_config reads it back."},
		mcpCallTimeout, exportConfig)
	addTool(srv, c, &mcp.Tool{Name: "import_config", Annotations: changes("Import configuration text", true, true),
		Description: "Administrators only. Plan importing configuration text (as export_config writes it): what would be created, changed and, with prune true, deleted. With apply true and the plan's digest, make exactly those changes."},
		mcpCallTimeout, importConfig)
	addTool(srv, c, &mcp.Tool{Name: "list_config_imports", Annotations: readOnly("List configuration imports"),
		Description: "The imports of configuration so far, the first start's import of the configuration file among them, with what each changed."},
		mcpCallTimeout, listConfigImports)
	addTool(srv, c, &mcp.Tool{Name: "check_repository", Annotations: changes("Check a Git repository", false, true),
		Description: "Read a repository as the server would, keeping nothing: whether it can be reached, its branches and default branch, and whether a branch has a pipeline file. Use it before creating a Flight. The server contacts the address (an internal address or a path on the server is refused unless allowed), so this is audited and limited, and is not a read-only call."},
		mcpCallTimeout, checkRepository)
	addTool(srv, c, &mcp.Tool{Name: "check_signin", Annotations: changes("Check sign-in settings", false, true),
		Description: "Check sign-in answers (issuer, client_id, redirect_url, client_secret_env, groups, first administrators) against the identity provider and list every problem. The server contacts the address, so an address inside its own network is refused unless an administrator allowed the host; the call is audited and limited."},
		mcpCallTimeout, checkSignIn)
}

type obj = map[string]any

// noIn is the input of a tool that takes none.
type noIn struct{}

type kindIn struct {
	Kind    string `json:"kind" jsonschema:"the kind: projects, libraries, environments, runner-pools, role-bindings, policies, policy-sources, health-sources, models, secret-stores, units, installation or organization"`
	Deleted bool   `json:"deleted,omitempty" jsonschema:"list the deleted objects instead"`
}

type nameIn struct {
	Kind string `json:"kind" jsonschema:"the kind, as for list_config"`
	Name string `json:"name" jsonschema:"the object's name; the Base settings are named installation, the Wing default"`
}

func needKind(k string) error {
	if k == "" {
		return errors.New("say which kind (list_config explains)")
	}
	return nil
}

func getConfigOverview(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (obj, error) {
	var out obj
	err := c.apiJSON(ctx, "get_config_overview", http.MethodGet, "/api/v1/config", nil, &out)
	return out, err
}

func listConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in kindIn) (obj, error) {
	var out obj
	if err := needKind(in.Kind); err != nil {
		return out, err
	}
	p := apiPath("/api/v1/config/%s", in.Kind)
	if in.Deleted {
		p += "?deleted=1"
	}
	err := c.apiJSON(ctx, "list_config", http.MethodGet, p, nil, &out)
	return out, err
}

func getConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in nameIn) (obj, error) {
	var out obj
	if err := needKind(in.Kind); err != nil {
		return out, err
	}
	if in.Name == "" {
		return out, errors.New("say which object: name")
	}
	err := c.apiJSON(ctx, "get_config", http.MethodGet, apiPath("/api/v1/config/%s/%s", in.Kind, in.Name), nil, &out)
	return out, err
}

type putIn struct {
	Kind          string `json:"kind" jsonschema:"the kind, as for list_config"`
	Name          string `json:"name" jsonschema:"the object's name"`
	Unit          string `json:"unit,omitempty" jsonschema:"the Squadron (unit) the object belongs to or is defined at; empty is the Wing"`
	Spec          any    `json:"spec" jsonschema:"the object's settings, with the keys the configuration file uses (get_config shows an example)"`
	Version       int64  `json:"version,omitempty" jsonschema:"the version you read; the change is refused if the object has moved on; -1 means it must not exist yet"`
	Overwrite     bool   `json:"overwrite,omitempty" jsonschema:"replace the object whatever version it is at (the last writer wins); only when you mean to discard what somebody else may have changed. Without it, changing an object that exists needs version"`
	Comment       string `json:"comment,omitempty" jsonschema:"why, for the history"`
	TakeOwnership bool   `json:"take_ownership,omitempty" jsonschema:"change an object that Terraform or Git manages, and become its owner; recorded"`
}

func putConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in putIn) (obj, error) {
	var out obj
	if err := needKind(in.Kind); err != nil {
		return out, err
	}
	if in.Name == "" {
		return out, errors.New("say which object: name")
	}
	if in.Spec == nil {
		return out, errors.New("give the object's settings: spec")
	}
	body := obj{"unit": in.Unit, "spec": in.Spec, "comment": in.Comment}
	if in.Version == 0 && !in.Overwrite {
		// Changing what nobody has read overwrites what somebody else may
		// have changed: a new object is created, an existing one needs the
		// version that was read.
		var cur struct {
			Version int64 `json:"version"`
		}
		err := c.apiJSON(ctx, "put_config", http.MethodGet, apiPath("/api/v1/config/%s/%s", in.Kind, in.Name), nil, &cur)
		switch {
		case err == nil && cur.Version > 0:
			return out, fmt.Errorf("%s %s exists, at version %d: read it with get_config and pass version %d, or pass overwrite true to replace it whatever it is", in.Kind, in.Name, cur.Version, cur.Version)
		case err != nil && !strings.Contains(err.Error(), "not found"):
			return out, err
		}
		in.Version = -1
	}
	if in.TakeOwnership {
		body["take_ownership"] = true
	}
	if in.Version != 0 {
		body["version"] = in.Version
	}
	if in.Version == -1 { // must not exist: the create route
		delete(body, "version")
		body["name"] = in.Name
		err := c.apiJSON(ctx, "put_config", http.MethodPost, apiPath("/api/v1/config/%s", in.Kind), body, &out)
		return out, err
	}
	err := c.apiJSON(ctx, "put_config", http.MethodPut, apiPath("/api/v1/config/%s/%s", in.Kind, in.Name), body, &out)
	return out, err
}

type lifecycleIn struct {
	Kind          string `json:"kind" jsonschema:"the kind, as for list_config"`
	Name          string `json:"name" jsonschema:"the object's name"`
	Version       int64  `json:"version,omitempty" jsonschema:"the version you read with get_config; refused if the object has moved on. Required unless overwrite is true"`
	Overwrite     bool   `json:"overwrite,omitempty" jsonschema:"act whatever version the object is at (the last writer wins)"`
	Comment       string `json:"comment,omitempty" jsonschema:"why, for the history"`
	ForceInUse    bool   `json:"force_in_use,omitempty" jsonschema:"delete even though active runs depend on it (Flight, environment, Ramp); recorded in the history and the audit trail"`
	TakeOwnership bool   `json:"take_ownership,omitempty" jsonschema:"change an object that Terraform or Git manages, and become its owner; recorded"`
}

func deleteConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in lifecycleIn) (obj, error) {
	return lifecycle(ctx, c, "delete_config", http.MethodDelete, "", in)
}

func restoreConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in lifecycleIn) (obj, error) {
	return lifecycle(ctx, c, "restore_config", http.MethodPost, "/restore", in)
}

func lifecycle(ctx context.Context, c *mcpCall, tool, method, suffix string, in lifecycleIn) (obj, error) {
	var out obj
	if err := needKind(in.Kind); err != nil {
		return out, err
	}
	if in.Name == "" {
		return out, errors.New("say which object: name")
	}
	if in.Version == 0 && !in.Overwrite {
		return out, errors.New("pass version (from get_config), so that the change is refused if somebody changed the object since you read it; or overwrite true to act whatever version it is at")
	}
	body := obj{"comment": in.Comment}
	if in.Version != 0 {
		body["version"] = in.Version
	}
	if in.ForceInUse {
		body["force_in_use"] = true
	}
	if in.TakeOwnership {
		body["take_ownership"] = true
	}
	err := c.apiJSON(ctx, tool, method, apiPath("/api/v1/config/%s/%s", in.Kind, in.Name)+suffix, body, &out)
	return out, err
}

type historyIn struct {
	Kind   string `json:"kind,omitempty" jsonschema:"only this kind's changes"`
	Name   string `json:"name,omitempty" jsonschema:"only this object's changes (needs kind)"`
	Before int64  `json:"before,omitempty" jsonschema:"changes before this change id, to page back"`
	Limit  int    `json:"limit,omitempty" jsonschema:"at most this many; default 50"`
}

func getConfigHistory(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in historyIn) (obj, error) {
	var out obj
	q := url.Values{}
	if in.Before > 0 {
		q.Set("before", strconv.FormatInt(in.Before, 10))
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	p := "/api/v1/config/history"
	switch {
	case in.Kind != "" && in.Name != "":
		p = apiPath("/api/v1/config/%s/%s/history", in.Kind, in.Name)
	case in.Kind != "":
		q.Set("kind", in.Kind)
	}
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	err := c.apiJSON(ctx, "get_config_history", http.MethodGet, p, nil, &out)
	return out, err
}

type changesIn struct {
	Changes []changeIn `json:"changes" jsonschema:"the changes, at most 200"`
}

type changeIn struct {
	Kind          string `json:"kind" jsonschema:"the kind, as for list_config"`
	Name          string `json:"name" jsonschema:"the object's name"`
	Action        string `json:"action,omitempty" jsonschema:"put (the default), delete or restore"`
	ForceInUse    bool   `json:"force_in_use,omitempty" jsonschema:"for delete: even though active runs depend on the object"`
	TakeOwnership bool   `json:"take_ownership,omitempty" jsonschema:"change an object that Terraform or Git manages, and become its owner; recorded"`
	Unit          string `json:"unit,omitempty" jsonschema:"the Squadron (unit); empty is the Wing"`
	Spec          any    `json:"spec,omitempty" jsonschema:"the settings, for put"`
	Version       int64  `json:"version,omitempty" jsonschema:"the version expected; -1 means it must not exist yet"`
	Comment       string `json:"comment,omitempty"`
}

func validateConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in changesIn) (obj, error) {
	var out obj
	err := c.apiJSON(ctx, "validate_config", http.MethodPost, "/api/v1/config/validate", in, &out)
	return out, err
}

func applyConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in changesIn) (obj, error) {
	var out obj
	err := c.apiJSON(ctx, "apply_config", http.MethodPost, "/api/v1/config/apply", in, &out)
	return out, err
}

type effectiveIn struct {
	Project string `json:"project" jsonschema:"the Flight's name"`
}

func getEffectiveConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in effectiveIn) (obj, error) {
	var out obj
	if in.Project == "" {
		return out, errors.New("say which Flight")
	}
	err := c.apiJSON(ctx, "get_effective_config", http.MethodGet, apiPath("/api/v1/config/effective/%s", in.Project), nil, &out)
	return out, err
}

type exportOut struct {
	Text string `json:"text" jsonschema:"the configuration as YAML"`
}

func exportConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (exportOut, error) {
	ans, err := c.api(ctx, "export_config", http.MethodGet, "/api/v1/config/export", nil, 0)
	if err != nil {
		return exportOut{}, err
	}
	if ans.Status >= 400 {
		return exportOut{}, answerError(ans)
	}
	return exportOut{Text: string(ans.Body)}, nil
}

type importIn struct {
	Text   string `json:"text" jsonschema:"the configuration text, as export_config writes it"`
	Prune  bool   `json:"prune,omitempty" jsonschema:"also delete objects the text does not have"`
	Apply  bool   `json:"apply,omitempty" jsonschema:"make the changes; without it, only plan them"`
	Digest string `json:"digest,omitempty" jsonschema:"the plan's digest, so exactly the plan you read is applied"`
}

func importConfig(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in importIn) (obj, error) {
	var out obj
	body := obj{"text": in.Text, "prune": in.Prune, "apply": in.Apply, "digest": in.Digest, "origin": "mcp"}
	err := c.apiJSON(ctx, "import_config", http.MethodPost, "/api/v1/config/import", body, &out)
	return out, err
}

func listConfigImports(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (obj, error) {
	var out obj
	err := c.apiJSON(ctx, "list_config_imports", http.MethodGet, "/api/v1/config/imports", nil, &out)
	return out, err
}

type checkRepoIn struct {
	Repo   string `json:"repo" jsonschema:"the repository's address (https, ssh, or a path on the server)"`
	Branch string `json:"branch,omitempty" jsonschema:"the branch to look at; default the repository's default branch"`
}

func checkRepository(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in checkRepoIn) (obj, error) {
	var out obj
	err := c.apiJSON(ctx, "check_repository", http.MethodPost, "/api/v1/config/check/repository", in, &out)
	return out, err
}

type checkSignInIn struct {
	Issuer          string   `json:"issuer" jsonschema:"the identity provider's issuer address"`
	ClientID        string   `json:"client_id" jsonschema:"the OpenID Connect client ID the provider gave Launchway"`
	RedirectURL     string   `json:"redirect_url" jsonschema:"this server's address followed by /auth/callback"`
	ClientSecretEnv string   `json:"client_secret_env" jsonschema:"the name of the environment variable that will hold the client secret; never the secret"`
	GroupsClaim     string   `json:"groups_claim,omitempty" jsonschema:"the claim that lists a person's groups, groups if left out"`
	UsernameClaim   string   `json:"username_claim,omitempty" jsonschema:"the claim used as the person's name"`
	AdminGroups     []string `json:"admin_groups,omitempty" jsonschema:"groups whose members become administrators"`
	AdminUsers      []string `json:"admin_users,omitempty" jsonschema:"usernames that become administrators"`
}

func checkSignIn(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in checkSignInIn) (obj, error) {
	var out obj
	err := c.apiJSON(ctx, "check_signin", http.MethodPost, "/api/v1/config/check/signin", in, &out)
	return out, err
}
