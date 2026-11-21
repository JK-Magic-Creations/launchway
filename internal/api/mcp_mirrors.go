package api

// The MCP tools for mirrors of hosted repositories: listing them with their
// status, setting one up, running one now and making a mirror the primary.
// Each calls the same routes as the API, so what a caller may do is what the
// API lets them do, and changes are audited. A credential is only ever a
// reference (the name of an environment variable or the path of a file on the
// server), never a value.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

// mirrorSyncTimeout bounds a tool call that runs a mirror: a large first
// sync takes a while.
const mirrorSyncTimeout = 15 * time.Minute

func addMirrorTools(srv *mcp.Server, c *mcpCall) {
	addImportTools(srv, c)
	addMirrorChangeTools(srv, c)
	addTool(srv, c, &mcp.Tool{Name: "list_mirrors", Annotations: readOnly("List mirrors"),
		Description: "The pull and push mirrors of hosted repositories the caller may read, each with its status: state (pending, ok, error), last_success, last_error (credentials removed), failures, next_run, lag_seconds and behind. With behind true, only the mirrors that have fallen behind."},
		mcpCallTimeout, listMirrors)
	if !c.mayOperate {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "set_mirror", Annotations: changes("Set up a mirror", false, true),
		Description: "Repository admins only. Make a hosted repository a pull mirror of an https address (direction pull, the default: an exact copy of every branch, tag and note, kept current on a schedule, and the repository takes no pushes meanwhile) or add a push mirror (direction push: every accepted update is copied to the address). The credential is a reference to a secret on the server, credential_env (the name of an environment variable) or credential_file (an absolute path), never the secret itself; username goes with it. A pull mirror needs a repository with no branches or tags of its own. Internal addresses are refused unless the server allows them. Audited."},
		mcpCallTimeout, setMirror)
	addTool(srv, c, &mcp.Tool{Name: "sync_mirror", Annotations: changes("Run a mirror now", false, true),
		Description: "Run a mirror now (needs the operator role on the repository) and return its status. A failed run returns the redacted error. Audited."},
		mirrorSyncTimeout, syncMirror)
	addTool(srv, c, &mcp.Tool{Name: "stop_mirroring", Annotations: changes("Stop mirroring", true, false),
		Description: "Repository admins only. Stop a repository being a pull mirror and make it the primary: it takes pushes from now on and the mirror is deleted. With final_sync, run the mirror once more first, and change nothing if that fails. This is the cut-over step of moving repositories to Launchway. Audited."},
		mirrorSyncTimeout, stopMirroring)
}

type listMirrorsIn struct {
	Behind bool `json:"behind,omitempty" jsonschema:"only mirrors that have fallen behind"`
}

func listMirrors(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in listMirrorsIn) (forge.Page[mirror.View], error) {
	var out forge.Page[mirror.View]
	path := "/api/v1/mirrors"
	if in.Behind {
		path += "?behind=true"
	}
	err := c.apiJSON(ctx, "list_mirrors", http.MethodGet, path, nil, &out)
	return out, err
}

type setMirrorIn struct {
	Repository      string `json:"repository" jsonschema:"the repository, owner/name"`
	Direction       string `json:"direction,omitempty" jsonschema:"pull (the default) or push"`
	URL             string `json:"url" jsonschema:"the https address of the other repository, with no user name or password in it"`
	Username        string `json:"username,omitempty" jsonschema:"the user name that goes with the credential (not a secret)"`
	CredentialEnv   string `json:"credential_env,omitempty" jsonschema:"the NAME of an environment variable of the server that holds the token"`
	CredentialFile  string `json:"credential_file,omitempty" jsonschema:"the absolute path of a file on the server that holds the token"`
	IntervalSeconds int64  `json:"interval_seconds,omitempty" jsonschema:"how often it runs, at least 60; default 3600 for pull"`
	Prune           *bool  `json:"prune,omitempty" jsonschema:"delete refs the other side no longer has; default true"`
}

func splitRepo(repo string) (owner, name string, err error) {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			if i == 0 || i == len(repo)-1 {
				break
			}
			return repo[:i], repo[i+1:], nil
		}
	}
	return "", "", errors.New("say which repository as owner/name")
}

func setMirror(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setMirrorIn) (mirror.View, error) {
	var out mirror.View
	owner, name, err := splitRepo(in.Repository)
	if err != nil {
		return out, err
	}
	body := map[string]any{"direction": in.Direction, "url": in.URL, "username": in.Username, "prune": in.Prune}
	if in.IntervalSeconds != 0 {
		body["interval_seconds"] = in.IntervalSeconds
	}
	switch {
	case in.CredentialEnv != "":
		body["credential"] = map[string]string{"env": in.CredentialEnv}
	case in.CredentialFile != "":
		body["credential"] = map[string]string{"file": in.CredentialFile}
	}
	err = c.apiJSON(ctx, "set_mirror", http.MethodPost, apiPath("/api/v1/repositories/%s/%s/mirrors", owner, name), body, &out)
	return out, err
}

type syncMirrorIn struct {
	Repository string `json:"repository" jsonschema:"the repository, owner/name"`
	MirrorID   string `json:"mirror_id" jsonschema:"the mirror's ID (mir_...), from list_mirrors"`
}

func syncMirror(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in syncMirrorIn) (mirror.View, error) {
	var out mirror.View
	owner, name, err := splitRepo(in.Repository)
	if err != nil {
		return out, err
	}
	err = c.apiJSON(ctx, "sync_mirror", http.MethodPost, apiPath("/api/v1/repositories/%s/%s/mirrors/%s/sync", owner, name, in.MirrorID), nil, &out)
	return out, err
}

type stopMirroringIn struct {
	Repository string `json:"repository" jsonschema:"the repository, owner/name"`
	FinalSync  bool   `json:"final_sync,omitempty" jsonschema:"run the mirror once more first; nothing changes if that fails"`
}

func stopMirroring(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in stopMirroringIn) (forge.Repository, error) {
	var out forge.Repository
	owner, name, err := splitRepo(in.Repository)
	if err != nil {
		return out, err
	}
	err = c.apiJSON(ctx, "stop_mirroring", http.MethodPost, apiPath("/api/v1/repositories/%s/%s/stop-mirroring", owner, name), map[string]any{"final_sync": in.FinalSync}, &out)
	return out, err
}
