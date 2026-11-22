package api

// Two more tools of the mirror and import group, so that every route of them
// has an MCP tool as well as a CLI command: pausing, resuming or removing a
// mirror, and removing a source connection.

import (
	"context"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

func addMirrorChangeTools(srv *mcp.Server, c *mcpCall) {
	if c.mayOperate {
		addTool(srv, c, &mcp.Tool{Name: "change_mirror", Annotations: changes("Pause, resume, change or remove a mirror", true, true),
			Description: "Repository admins only. pause or resume a mirror's schedule, update a push mirror's outside-release settings (publish_releases on or off, outside_api), or remove it (a push mirror is simply deleted; to end a pull mirror and make the repository the primary use stop_mirroring). The mirror's ID is in list_mirrors. Audited."},
			mcpCallTimeout, changeMirror)
	}
	if c.mayAdminister {
		addTool(srv, c, &mcp.Tool{Name: "remove_connection", Annotations: changes("Remove a source connection", true, true),
			Description: "Administrators only. Forget a connection to a server repositories are imported from. Repositories already imported and their mirrors are not touched. Audited."},
			mcpCallTimeout, removeConnection)
	}
}

type changeMirrorIn struct {
	Repository string `json:"repository" jsonschema:"the repository, owner/name"`
	MirrorID   string `json:"mirror_id" jsonschema:"the mirror's ID (mir_...), from list_mirrors"`
	Action     string `json:"action" jsonschema:"pause, resume, update or remove"`
	// For update: a nil field is left as it is.
	PublishReleases *bool   `json:"publish_releases,omitempty" jsonschema:"update: make (true) or stop making (false) the release on the outside Git host after a release is published"`
	OutsideAPI      *string `json:"outside_api,omitempty" jsonschema:"update: the address of the outside host's API; empty derives it from the mirror's address"`
}

type changeMirrorOut struct {
	Removed string       `json:"removed,omitempty"`
	Mirror  *mirror.View `json:"mirror,omitempty"`
}

func changeMirror(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in changeMirrorIn) (changeMirrorOut, error) {
	var out changeMirrorOut
	owner, name, err := splitRepo(in.Repository)
	if err != nil {
		return out, err
	}
	path := apiPath("/api/v1/repositories/%s/%s/mirrors/%s", owner, name, in.MirrorID)
	switch in.Action {
	case "remove":
		if err := c.apiJSON(ctx, "change_mirror", http.MethodDelete, path, nil, nil); err != nil {
			return out, err
		}
		out.Removed = in.MirrorID
	case "pause", "resume":
		var v mirror.View
		if err := c.apiJSON(ctx, "change_mirror", http.MethodPatch, path, map[string]any{"enabled": in.Action == "resume"}, &v); err != nil {
			return out, err
		}
		out.Mirror = &v
	case "update":
		body := map[string]any{}
		if in.PublishReleases != nil {
			body["publish_releases"] = *in.PublishReleases
		}
		if in.OutsideAPI != nil {
			body["outside_api"] = *in.OutsideAPI
		}
		if len(body) == 0 {
			return out, errors.New("say what to update: publish_releases or outside_api")
		}
		var v mirror.View
		if err := c.apiJSON(ctx, "change_mirror", http.MethodPatch, path, body, &v); err != nil {
			return out, err
		}
		out.Mirror = &v
	default:
		return out, errors.New("action is pause, resume, update or remove")
	}
	return out, nil
}

type removeConnectionIn struct {
	Name string `json:"name" jsonschema:"the connection's name, from list_connections"`
}

type removeConnectionOut struct {
	Removed string `json:"removed"`
}

func removeConnection(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in removeConnectionIn) (removeConnectionOut, error) {
	if in.Name == "" {
		return removeConnectionOut{}, errors.New("say which connection")
	}
	if err := c.apiJSON(ctx, "remove_connection", http.MethodDelete, apiPath("/api/v1/connections/%s", in.Name), nil, nil); err != nil {
		return removeConnectionOut{}, err
	}
	return removeConnectionOut{Removed: in.Name}, nil
}
