package api

// The MCP tools for Git hosting repositories (schema 26, forge.hosting on):
// list and read them, make one, change one and delete one. Each calls the
// same routes as the API, so the answer for a caller is the API's: a
// repository one may not read is reported as one that does not exist, and
// with hosting off every tool says so (feature_off) and nothing is lost.

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

func addRepositoryTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_repositories", Annotations: readOnly("List hosted repositories"),
		Description: "The Git repositories this server hosts that you may read: owner/name path, default branch, visibility, whether archived, and the address to clone from. Answers feature_off when Git hosting is not turned on (installation setting forge.hosting)."},
		mcpCallTimeout, listRepositories)
	addTool(srv, c, &mcp.Tool{Name: "get_repository", Annotations: readOnly("Get a hosted repository"),
		Description: "One hosted repository by owner and name."},
		mcpCallTimeout, getRepository)
	if c.mayOperate {
		addTool(srv, c, &mcp.Tool{Name: "update_repository", Annotations: changes("Change a hosted repository", true, true),
			Description: "Repository administrators only. Change a repository's visibility (private, internal or public), default branch or archived flag, or rename it with new_owner and new_name; the old address stops working. A field left out keeps its value. Audited."},
			mcpCallTimeout, updateRepository)
		addTool(srv, c, &mcp.Tool{Name: "delete_repository", Annotations: changes("Delete a hosted repository", true, true),
			Description: "Repository administrators only. Move a repository to the trash: it is no longer served or listed, its data is kept, and its name can be used again. Audited."},
			mcpCallTimeout, deleteRepository)
	}
	if c.mayAdminister {
		addTool(srv, c, &mcp.Tool{Name: "create_repository", Annotations: changes("Create a hosted repository", false, false),
			Description: "Administrators only. Create an empty hosted repository: owner (default: the installation's default owner), name, default_branch (main) and visibility (private). Then push to the clone address with a token. Audited."},
			mcpCallTimeout, createRepository)
	}
	addKeyTools(srv, c)
	addArchiveTools(srv, c)
	addFilesTools(srv, c)
}

type repositoryOut struct {
	Path          string `json:"path"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Visibility    string `json:"visibility"`
	Archived      bool   `json:"archived"`
	CloneHTTPS    string `json:"clone_https,omitempty"`
}

func repositoryView(r forge.Repository) repositoryOut {
	return repositoryOut{Path: untrusted(r.Path, 200), Owner: untrusted(r.Home, 100), Name: untrusted(r.Name, 100),
		DefaultBranch: untrusted(r.DefaultBranch, 100), Visibility: string(r.Visibility), Archived: r.Archived, CloneHTTPS: r.Clone.HTTPS}
}

type repositoriesOut struct {
	Summary      string          `json:"summary"`
	Repositories []repositoryOut `json:"repositories"`
}

func listRepositories(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (repositoriesOut, error) {
	var page forge.Page[forge.Repository]
	if err := c.apiJSON(ctx, "list_repositories", http.MethodGet, "/api/v1/repositories", nil, &page); err != nil {
		return repositoriesOut{}, err
	}
	out := repositoriesOut{Repositories: []repositoryOut{}}
	for _, r := range page.Items {
		out.Repositories = append(out.Repositories, repositoryView(r))
	}
	out.Summary = fmt.Sprintf("%d hosted repositories", len(out.Repositories))
	return out, nil
}

type repositoryIn struct {
	Owner string `json:"owner" jsonschema:"the repository's owner, the first part of owner/name"`
	Name  string `json:"name" jsonschema:"the repository's name"`
}

func getRepository(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repositoryIn) (repositoryOut, error) {
	if in.Owner == "" || in.Name == "" {
		return repositoryOut{}, errors.New("say which repository: owner and name")
	}
	var r forge.Repository
	if err := c.apiJSON(ctx, "get_repository", http.MethodGet, apiPath("/api/v1/repositories/%s/%s", in.Owner, in.Name), nil, &r); err != nil {
		return repositoryOut{}, err
	}
	return repositoryView(r), nil
}

type createRepositoryIn struct {
	Owner         string `json:"owner,omitempty" jsonschema:"the owner; the installation's default owner if left out"`
	Name          string `json:"name" jsonschema:"the new repository's name"`
	DefaultBranch string `json:"default_branch,omitempty" jsonschema:"the default branch (main if left out)"`
	Visibility    string `json:"visibility,omitempty" jsonschema:"private (default), internal or public"`
}

func createRepository(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in createRepositoryIn) (repositoryOut, error) {
	if in.Name == "" {
		return repositoryOut{}, errors.New("say the repository's name")
	}
	var r forge.Repository
	err := c.apiJSON(ctx, "create_repository", http.MethodPost, "/api/v1/repositories", map[string]any{"owner": in.Owner, "name": in.Name,
		"default_branch": in.DefaultBranch, "visibility": in.Visibility}, &r)
	return repositoryView(r), err
}

type updateRepositoryIn struct {
	Owner         string  `json:"owner" jsonschema:"the repository's current owner"`
	Name          string  `json:"name" jsonschema:"the repository's current name"`
	NewOwner      *string `json:"new_owner,omitempty" jsonschema:"move it to this owner"`
	NewName       *string `json:"new_name,omitempty" jsonschema:"rename it"`
	Visibility    *string `json:"visibility,omitempty" jsonschema:"private, internal or public"`
	DefaultBranch *string `json:"default_branch,omitempty" jsonschema:"the default branch"`
	Archived      *bool   `json:"archived,omitempty" jsonschema:"archive it (no pushes) or bring it back"`
}

func updateRepository(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in updateRepositoryIn) (repositoryOut, error) {
	if in.Owner == "" || in.Name == "" {
		return repositoryOut{}, errors.New("say which repository: owner and name")
	}
	body := map[string]any{}
	set := func(k string, v any, ok bool) {
		if ok {
			body[k] = v
		}
	}
	if in.NewOwner != nil {
		set("owner", *in.NewOwner, true)
	}
	if in.NewName != nil {
		set("name", *in.NewName, true)
	}
	if in.Visibility != nil {
		set("visibility", *in.Visibility, true)
	}
	if in.DefaultBranch != nil {
		set("default_branch", *in.DefaultBranch, true)
	}
	if in.Archived != nil {
		set("archived", *in.Archived, true)
	}
	if len(body) == 0 {
		return repositoryOut{}, errors.New("say what to change: new_owner, new_name, visibility, default_branch or archived")
	}
	var r forge.Repository
	err := c.apiJSON(ctx, "update_repository", http.MethodPatch, apiPath("/api/v1/repositories/%s/%s", in.Owner, in.Name), body, &r)
	return repositoryView(r), err
}

type deletedRepositoryOut struct {
	Deleted string `json:"deleted"`
}

func deleteRepository(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repositoryIn) (deletedRepositoryOut, error) {
	if in.Owner == "" || in.Name == "" {
		return deletedRepositoryOut{}, errors.New("say which repository: owner and name")
	}
	err := c.apiJSON(ctx, "delete_repository", http.MethodDelete, apiPath("/api/v1/repositories/%s/%s", in.Owner, in.Name), nil, nil)
	if err != nil {
		return deletedRepositoryOut{}, err
	}
	return deletedRepositoryOut{Deleted: in.Owner + "/" + in.Name}, nil
}
