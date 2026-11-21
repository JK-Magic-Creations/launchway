package api

// The MCP tools for getting repositories into Launchway: connections to the
// servers they are on, import, and the verification that every ref equals the
// source's. Administrators only, as the API requires; each calls the same
// routes, so each is audited.

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

func addImportTools(srv *mcp.Server, c *mcpCall) {
	if !c.mayAdminister {
		return
	}
	addVerifyRepositoriesTool(srv, c)
	addTool(srv, c, &mcp.Tool{Name: "list_connections", Annotations: readOnly("List source connections"),
		Description: "Administrators only. The connections to servers that repositories can be imported from: name, kind (gitea, forgejo or plain), base_url and where the credential is read from (never the credential)."},
		mcpCallTimeout, listConnections)
	addTool(srv, c, &mcp.Tool{Name: "add_connection", Annotations: changes("Add a source connection", false, false),
		Description: "Administrators only. Record a server to import from: a name, a kind (gitea or forgejo, whose API lists the repositories; plain for a list of addresses), its base_url and a credential reference (credential_env: the NAME of an environment variable of the server, or credential_file: an absolute path), never the token itself. An internal address is refused unless the server allows it (forge.mirror_allow). Audited."},
		mcpCallTimeout, addConnection)
	addTool(srv, c, &mcp.Tool{Name: "import_repositories", Annotations: changes("Import repositories", false, true),
		Description: "Administrators only. Bring repositories in from a connection (or a list of urls): each is made with the same owner, name and visibility, every branch, tag and note is copied and checked ref by ref, and each is left a pull mirror (mirror true, the default: the staged move; its source stays the primary until stop_mirroring). dry_run lists what would be created and changes nothing. Idempotent: a repository already imported is resumed, one that exists otherwise is left alone, a failure of one does not stop the others. With wait, each is copied and checked before the answer; do that for one repository or a few at a time. Audited."},
		mirrorSyncTimeout, importRepositories)
}

func listConnections(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ getStepResourcesIn) (forge.Page[mirror.ConnectionView], error) {
	var out forge.Page[mirror.ConnectionView]
	err := c.apiJSON(ctx, "list_connections", http.MethodGet, "/api/v1/connections", nil, &out)
	return out, err
}

type addConnectionIn struct {
	Name           string `json:"name" jsonschema:"a short name, lower case letters, digits, . _ -"`
	Kind           string `json:"kind" jsonschema:"gitea, forgejo or plain"`
	BaseURL        string `json:"base_url,omitempty" jsonschema:"the server's address, https://git.example.com"`
	CredentialEnv  string `json:"credential_env,omitempty" jsonschema:"the NAME of an environment variable of the server that holds the token"`
	CredentialFile string `json:"credential_file,omitempty" jsonschema:"the absolute path of a file on the server that holds the token"`
}

func addConnection(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in addConnectionIn) (mirror.ConnectionView, error) {
	var out mirror.ConnectionView
	body := map[string]any{"name": in.Name, "kind": in.Kind, "base_url": in.BaseURL}
	switch {
	case in.CredentialEnv != "":
		body["credential"] = map[string]string{"env": in.CredentialEnv}
	case in.CredentialFile != "":
		body["credential"] = map[string]string{"file": in.CredentialFile}
	}
	err := c.apiJSON(ctx, "add_connection", http.MethodPost, "/api/v1/connections", body, &out)
	return out, err
}

type sourceIn struct {
	Connection  string   `json:"connection,omitempty" jsonschema:"the name of a connection (list_connections)"`
	URLs        []string `json:"urls,omitempty" jsonschema:"for a plain connection or none: repository addresses to import"`
	Owner       string   `json:"owner,omitempty" jsonschema:"only this user or organization of the source"`
	Repository  string   `json:"repository,omitempty" jsonschema:"only this repository of the source, owner/name"`
	TargetOwner string   `json:"target_owner,omitempty" jsonschema:"make them under this owner here instead of the source's owner names"`
	Username    string   `json:"username,omitempty" jsonschema:"the user name that goes with the connection's credential on the mirrors made"`
}

type repoImportIn struct {
	sourceIn
	Mirror          *bool `json:"mirror,omitempty" jsonschema:"leave each a pull mirror (default true); false makes each an ordinary repository once copied and checked, and needs wait"`
	IntervalSeconds int64 `json:"interval_seconds,omitempty" jsonschema:"the mirrors' schedule, at least 60; default 3600"`
	DryRun          bool  `json:"dry_run,omitempty" jsonschema:"list what would be created and change nothing"`
	Wait            bool  `json:"wait,omitempty" jsonschema:"copy and check each repository before answering"`
}

func importRepositories(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repoImportIn) (mirror.ImportResult, error) {
	var out mirror.ImportResult
	err := c.apiJSON(ctx, "import_repositories", http.MethodPost, "/api/v1/imports", in, &out)
	return out, err
}
