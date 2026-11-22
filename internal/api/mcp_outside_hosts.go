package api

// The MCP tools for keeping code on another Git host: connections to it and
// the Flights attached to a repository there. Administrators only, as the API
// requires; each calls the same routes, so each is audited.

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
)

func addOutsideHostTools(srv *mcp.Server, c *mcpCall) {
	if !c.mayAdminister {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "list_outside_hosts", Annotations: readOnly("List outside Git host connections"),
		Description: "Administrators only. The connections to outside Git hosts (a host that keeps the code while Launchway runs the pipelines): name, kind, base_url, where the credential is read from (never the credential), the owners allowed, and what the host can and cannot do."},
		mcpCallTimeout, listOutsideHosts)
	addTool(srv, c, &mcp.Tool{Name: "add_outside_host", Annotations: changes("Connect an outside Git host", false, false),
		Description: "Administrators only. Connect a Git host Launchway does not host (kind github, gitlab, gitea, forgejo, bitbucket, bitbucket-datacenter or plain (any Git server over https, base_url required, a token or a user name and password as the credential, followed by polling): the public service, or a self-managed install through base_url). The credential is a reference: credential_env (the NAME of an environment variable of the server) or credential_file (an absolute path), or sealed:NAME for a token kept sealed; never the token. webhook_secret_env or webhook_secret_file names the secret that signs the host's webhooks. owners limits which owners or groups Flights may use. An internal address is refused unless the server allows it (forge.mirror_allow). Audited."},
		mcpCallTimeout, addOutsideHost)
	addTool(srv, c, &mcp.Tool{Name: "remove_outside_host", Annotations: changes("Remove an outside Git host connection", true, true),
		Description: "Administrators only. Forget a connection to an outside Git host. Refused while a Flight still keeps its code on it. Audited."},
		mcpCallTimeout, removeOutsideHost)
	addTool(srv, c, &mcp.Tool{Name: "test_outside_host", Annotations: readOnly("Test an outside Git host connection"),
		Description: "Administrators only. Ask the host who the credential is, when the token expires (when the host says), and which of the permissions each feature needs it has and lacks, in plain words. Reads only; audited."},
		mcpCallTimeout, testOutsideHost)
	addTool(srv, c, &mcp.Tool{Name: "list_outside_host_repositories", Annotations: readOnly("List repositories on an outside Git host"),
		Description: "Administrators only. The repositories the connection's credential reaches (narrowed to the allowed owners), with a text search on the path and a cursor for the next page."},
		mcpCallTimeout, listOutsideHostRepositories)
	addTool(srv, c, &mcp.Tool{Name: "get_project_outside", Annotations: readOnly("Get a Flight's outside repository"),
		Description: "Administrators only. The repository on an outside Git host a Flight is attached to, its two report switches, and what that host cannot do (with the reason and what to do instead)."},
		mcpCallTimeout, getProjectOutside)
	addTool(srv, c, &mcp.Tool{Name: "set_project_outside", Annotations: changes("Attach a Flight to an outside repository", false, true),
		Description: "Administrators only. Attach a Flight (project) to a repository on an outside Git host through a connection, and set whether it writes a commit status for each Sortie (report_statuses) and one updated summary comment on pull requests (report_comment). Audited."},
		mcpCallTimeout, setProjectOutside)
	addTool(srv, c, &mcp.Tool{Name: "detach_project_outside", Annotations: changes("Detach a Flight from an outside repository", true, true),
		Description: "Administrators only. Stop using the outside connection for a Flight; its Git address is kept. Audited."},
		mcpCallTimeout, detachProjectOutside)
}

func listOutsideHosts(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ getStepResourcesIn) (forge.Page[hosts.View], error) {
	var out forge.Page[hosts.View]
	err := c.apiJSON(ctx, "list_outside_hosts", http.MethodGet, "/api/v1/outside-hosts", nil, &out)
	return out, err
}

type addOutsideHostIn struct {
	Name              string   `json:"name" jsonschema:"a short name, lower case letters, digits, . _ -"`
	Kind              string   `json:"kind" jsonschema:"the kind of host: github, gitlab, gitea, forgejo, bitbucket, bitbucket-datacenter or plain (gitea, forgejo, bitbucket-datacenter and plain are always self-managed: give base_url)"`
	BaseURL           string   `json:"base_url,omitempty" jsonschema:"a self-managed install's address, https://git.example.com; empty is the public service"`
	UploadURL         string   `json:"upload_url,omitempty" jsonschema:"the host's upload address when it differs from the derived one"`
	CredentialEnv     string   `json:"credential_env,omitempty" jsonschema:"the NAME of an environment variable of the server that holds the token"`
	CredentialFile    string   `json:"credential_file,omitempty" jsonschema:"the absolute path of a file on the server that holds the token, or sealed:NAME"`
	WebhookSecretEnv  string   `json:"webhook_secret_env,omitempty" jsonschema:"the NAME of an environment variable that holds the webhook secret"`
	WebhookSecretFile string   `json:"webhook_secret_file,omitempty" jsonschema:"the absolute path of a file that holds the webhook secret"`
	Owners            []string `json:"owners,omitempty" jsonschema:"owners or groups Flights may use through this connection; empty is every owner the credential reaches"`
}

func addOutsideHost(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in addOutsideHostIn) (hosts.View, error) {
	var out hosts.View
	body := map[string]any{"name": in.Name, "kind": in.Kind, "base_url": in.BaseURL, "upload_url": in.UploadURL, "owners": in.Owners}
	switch {
	case in.CredentialEnv != "":
		body["credential"] = map[string]string{"env": in.CredentialEnv}
	case in.CredentialFile != "":
		body["credential"] = map[string]string{"file": in.CredentialFile}
	}
	switch {
	case in.WebhookSecretEnv != "":
		body["webhook_secret"] = map[string]string{"env": in.WebhookSecretEnv}
	case in.WebhookSecretFile != "":
		body["webhook_secret"] = map[string]string{"file": in.WebhookSecretFile}
	}
	err := c.apiJSON(ctx, "add_outside_host", http.MethodPost, "/api/v1/outside-hosts", body, &out)
	return out, err
}

type outsideHostNameIn struct {
	Name string `json:"name" jsonschema:"the connection's name (list_outside_hosts)"`
}

func removeOutsideHost(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in outsideHostNameIn) (removeConnectionOut, error) {
	if in.Name == "" {
		return removeConnectionOut{}, errors.New("say which connection")
	}
	if err := c.apiJSON(ctx, "remove_outside_host", http.MethodDelete, apiPath("/api/v1/outside-hosts/%s", in.Name), nil, nil); err != nil {
		return removeConnectionOut{}, err
	}
	return removeConnectionOut{Removed: in.Name}, nil
}

func testOutsideHost(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in outsideHostNameIn) (hosts.TestResult, error) {
	var out hosts.TestResult
	err := c.apiJSON(ctx, "test_outside_host", http.MethodPost, apiPath("/api/v1/outside-hosts/%s/test", in.Name), map[string]any{}, &out)
	return out, err
}

type outsideReposIn struct {
	Name   string `json:"name" jsonschema:"the connection's name (list_outside_hosts)"`
	Query  string `json:"query,omitempty" jsonschema:"only repositories whose path contains this text"`
	Cursor string `json:"cursor,omitempty" jsonschema:"the next value of a previous answer"`
}

func listOutsideHostRepositories(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in outsideReposIn) (forge.Page[forge.Repository], error) {
	var out forge.Page[forge.Repository]
	q := url.Values{"q": {in.Query}, "cursor": {in.Cursor}}.Encode()
	err := c.apiJSON(ctx, "list_outside_host_repositories", http.MethodGet, apiPath("/api/v1/outside-hosts/%s/repositories", in.Name)+"?"+q, nil, &out)
	return out, err
}

type projectOutsideIn struct {
	Project string `json:"project" jsonschema:"the Flight (project) name"`
}

func getProjectOutside(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in projectOutsideIn) (hosts.FlightView, error) {
	var out hosts.FlightView
	err := c.apiJSON(ctx, "get_project_outside", http.MethodGet, apiPath("/api/v1/projects/%s/outside", in.Project), nil, &out)
	return out, err
}

type setProjectOutsideIn struct {
	Project        string `json:"project" jsonschema:"the Flight (project) name"`
	Connection     string `json:"connection,omitempty" jsonschema:"the connection (list_outside_hosts); with repository, attaches or moves the Flight"`
	Repository     string `json:"repository,omitempty" jsonschema:"owner/name on the host"`
	ReportStatuses *bool  `json:"report_statuses,omitempty" jsonschema:"write a commit status for each Sortie"`
	ReportComment  *bool  `json:"report_comment,omitempty" jsonschema:"write one summary comment on pull requests, edited in place"`
}

func setProjectOutside(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setProjectOutsideIn) (hosts.FlightView, error) {
	var out hosts.FlightView
	err := c.apiJSON(ctx, "set_project_outside", http.MethodPut, apiPath("/api/v1/projects/%s/outside", in.Project),
		map[string]any{"connection": in.Connection, "repository": in.Repository, "report_statuses": in.ReportStatuses, "report_comment": in.ReportComment}, &out)
	return out, err
}

func detachProjectOutside(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in projectOutsideIn) (removeConnectionOut, error) {
	if err := c.apiJSON(ctx, "detach_project_outside", http.MethodDelete, apiPath("/api/v1/projects/%s/outside", in.Project), nil, nil); err != nil {
		return removeConnectionOut{}, err
	}
	return removeConnectionOut{Removed: in.Project}, nil
}
