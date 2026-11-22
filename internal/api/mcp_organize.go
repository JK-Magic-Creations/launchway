package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type organizeIn struct {
	Action      string          `json:"action,omitempty" jsonschema:"list (default), create_folder, update_folder, delete_folder, move, labels, get_layout, set_layout, reset_layout, export or import"`
	Area        string          `json:"area,omitempty" jsonschema:"pipelines, repositories, projects or runs"`
	Scope       string          `json:"scope,omitempty" jsonschema:"base, organization, unit:<name> or project:<name>; where a folder or layout lives"`
	Folder      string          `json:"folder,omitempty" jsonschema:"a folder's ID (update_folder, delete_folder); the folder to move an item into (move); empty takes the item out"`
	Parent      *string         `json:"parent,omitempty" jsonschema:"the parent folder's ID, for create_folder or update_folder"`
	Name        *string         `json:"name,omitempty" jsonschema:"a folder's name, no slash"`
	Description *string         `json:"description,omitempty" jsonschema:"a folder's description"`
	Item        string          `json:"item,omitempty" jsonschema:"the Flight or repository to move or label"`
	Labels      []string        `json:"labels,omitempty" jsonschema:"the labels an item should have; lower case, up to 20"`
	Page        string          `json:"page,omitempty" jsonschema:"the page of a layout: home, pipelines, projects, repositories or runs"`
	Layout      json.RawMessage `json:"layout,omitempty" jsonschema:"the layout document for set_layout, or the whole document for import"`
	Locked      bool            `json:"locked,omitempty" jsonschema:"set_layout: the levels below may not change it"`
	DryRun      bool            `json:"dry_run,omitempty" jsonschema:"import: say what would change"`
}

type organizeOut struct {
	Result any `json:"result" jsonschema:"what the API answered"`
}

func addOrganizeTool(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "organize", Annotations: changes("Organize lists", true, false),
		Description: "How the lists are organized: folders, labels and layouts shared by level, as the Explorer shows them. action list (default) reads one area's folders and each item's folder and labels; create_folder, update_folder, delete_folder, move and labels change the shared arrangement and need the right to arrange that level (audited); get_layout, set_layout and reset_layout read and change a page's shared layout; export and import move a level's layouts and folders as one document, for Git. Import only adds and updates."},
		mcpCallTimeout, organize)
}

func organize(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in organizeIn) (organizeOut, error) {
	var method, path string
	var body any
	area := in.Area
	if area == "" {
		area = "pipelines"
	}
	page := in.Page
	if page == "" {
		page = "home"
	}
	switch in.Action {
	case "", "list":
		method, path = http.MethodGet, "/api/v1/organize/"+url.PathEscape(area)
	case "create_folder":
		method, path = http.MethodPost, "/api/v1/organize/"+url.PathEscape(area)+"/folders"
		p := ""
		if in.Parent != nil {
			p = *in.Parent
		}
		n, d := "", ""
		if in.Name != nil {
			n = *in.Name
		}
		if in.Description != nil {
			d = *in.Description
		}
		body = map[string]any{"scope": in.Scope, "parent": p, "name": n, "description": d}
	case "update_folder":
		method, path = http.MethodPatch, "/api/v1/organize/folders/"+url.PathEscape(in.Folder)
		body = map[string]any{"name": in.Name, "description": in.Description, "parent": in.Parent}
	case "delete_folder":
		method, path = http.MethodDelete, "/api/v1/organize/folders/"+url.PathEscape(in.Folder)
	case "move":
		method, path = http.MethodPost, "/api/v1/organize/"+url.PathEscape(area)+"/move"
		body = map[string]any{"item": in.Item, "folder": in.Folder}
	case "labels":
		method, path = http.MethodPut, "/api/v1/organize/"+url.PathEscape(area)+"/labels"
		body = map[string]any{"item": in.Item, "labels": in.Labels}
	case "get_layout":
		method, path = http.MethodGet, "/api/v1/layouts/"+url.PathEscape(page)+"?scope="+url.QueryEscape(nzs(in.Scope, "base"))
	case "set_layout":
		method, path = http.MethodPut, "/api/v1/layouts/"+url.PathEscape(page)
		body = map[string]any{"scope": nzs(in.Scope, "base"), "data": in.Layout, "locked": in.Locked}
	case "reset_layout":
		method, path = http.MethodDelete, "/api/v1/layouts/"+url.PathEscape(page)+"?scope="+url.QueryEscape(nzs(in.Scope, "base"))
	case "export":
		method, path = http.MethodGet, "/api/v1/layouts-export?scope="+url.QueryEscape(nzs(in.Scope, "base"))
	case "import":
		method, path = http.MethodPost, "/api/v1/layouts-import"
		if in.DryRun {
			path += "?dry_run=1"
		}
		body = in.Layout
	default:
		return organizeOut{}, fmt.Errorf("unknown action %q", in.Action)
	}
	var out any
	if err := c.apiJSON(ctx, "organize", method, path, body, &out); err != nil {
		return organizeOut{}, err
	}
	return organizeOut{Result: out}, nil
}

func nzs(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
