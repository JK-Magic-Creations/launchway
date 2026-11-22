package api

// MCP tools of the repository trash (see forge_trash.go). Each calls the route of the API, so the answer for a
// caller is the API's.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

func addTrashTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_repository_trash", Annotations: readOnly("List deleted repositories"),
		Description: "The deleted repositories you administer: their id, the address they had, who deleted them and when, and when each will be removed for good (the retention is a hosting setting). An administrator of everything sees all. Answers feature_off when Git hosting is not turned on."},
		mcpCallTimeout, listRepositoryTrash)
	if c.mayOperate {
		addTool(srv, c, &mcp.Tool{Name: "restore_repository", Annotations: changes("Restore a deleted repository", false, true),
			Description: "Repository administrators only. Bring a deleted repository back by its id (from list_repository_trash), under the address it had; when another repository has taken that, give new_name (and, for administrators of everything, new_owner). Its refs, pull requests, keys and rules come back with it. Audited."},
			mcpCallTimeout, restoreRepository)
	}
	if c.mayAdminister {
		addTool(srv, c, &mcp.Tool{Name: "purge_repository", Annotations: changes("Remove a deleted repository for good", true, true),
			Description: "Installation administrators only. Remove a deleted repository for good: its Git storage (refs, packs and the pack blobs nothing else names) and its rows. This cannot be undone. confirm must be the address the repository had, typed exactly as listed by list_repository_trash. Audited."},
			mcpCallTimeout, purgeRepository)
	}
}

type trashedOut struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	DeletedBy string `json:"deleted_by,omitempty"`
	DeletedAt string `json:"deleted_at"`
	PurgeAt   string `json:"purge_at"`
	NameTaken bool   `json:"name_taken,omitempty"`
	Purging   bool   `json:"purging,omitempty"`
}

type trashListOut struct {
	Summary string       `json:"summary"`
	Trash   []trashedOut `json:"repositories"`
}

func listRepositoryTrash(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ noIn) (trashListOut, error) {
	var page forge.Page[forge.TrashedRepository]
	if err := c.apiJSON(ctx, "list_repository_trash", http.MethodGet, "/api/v1/repository-trash", nil, &page); err != nil {
		return trashListOut{}, err
	}
	out := trashListOut{Summary: fmt.Sprintf("%d deleted repositories", len(page.Items)), Trash: make([]trashedOut, 0, len(page.Items))}
	for _, t := range page.Items {
		out.Trash = append(out.Trash, trashedOut{ID: t.ID, Path: untrusted(t.Path, 200), DeletedBy: untrusted(t.DeletedBy, 100),
			DeletedAt: t.DeletedAt.UTC().Format(time.RFC3339), PurgeAt: t.PurgeAt.UTC().Format(time.RFC3339), NameTaken: t.NameTaken, Purging: t.Purging})
	}
	return out, nil
}

type restoreIn struct {
	ID       string `json:"id" jsonschema:"the id of the deleted repository (from list_repository_trash)"`
	NewOwner string `json:"new_owner,omitempty" jsonschema:"restore under another owner (administrators of everything only)"`
	NewName  string `json:"new_name,omitempty" jsonschema:"restore under another name, when the old address has been taken"`
}

func restoreRepository(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in restoreIn) (repositoryOut, error) {
	if in.ID == "" {
		return repositoryOut{}, errors.New("say which deleted repository: its id from list_repository_trash")
	}
	body := map[string]string{}
	if in.NewOwner != "" {
		body["owner"] = in.NewOwner
	}
	if in.NewName != "" {
		body["name"] = in.NewName
	}
	var r forge.Repository
	err := c.apiJSON(ctx, "restore_repository", http.MethodPost, apiPath("/api/v1/repository-trash/%s/restore", in.ID), body, &r)
	return repositoryView(r), err
}

type purgeIn struct {
	ID      string `json:"id" jsonschema:"the id of the deleted repository (from list_repository_trash)"`
	Confirm string `json:"confirm" jsonschema:"the address the repository had, typed exactly (owner/name)"`
}

func purgeRepository(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in purgeIn) (forge.PurgeSummary, error) {
	if in.ID == "" {
		return forge.PurgeSummary{}, errors.New("say which deleted repository: its id from list_repository_trash")
	}
	var sum forge.PurgeSummary
	err := c.apiJSON(ctx, "purge_repository", http.MethodDelete, apiPath("/api/v1/repository-trash/%s", in.ID), map[string]string{"confirm": in.Confirm}, &sum)
	return sum, err
}
