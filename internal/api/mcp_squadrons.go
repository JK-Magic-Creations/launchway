package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP tools for Squadrons (stored as units) and Flights (stored as projects). Tool names use the permanent names;
// the descriptions say Squadron and Flight. Each calls the same route the web page and the command line use.

type unitNameIn struct {
	Name string `json:"name" jsonschema:"the Squadron's name (its permanent name is unit)"`
}

type unitCreateIn struct {
	Name        string `json:"name" jsonschema:"a name with letters, digits, '.', '_' and '-'"`
	DisplayName string `json:"display_name,omitempty" jsonschema:"how the name is shown"`
	Description string `json:"description,omitempty" jsonschema:"what the Squadron is for"`
}

type candidatesIn struct {
	Query      string `json:"query,omitempty" jsonschema:"part of a repository's path"`
	Connection string `json:"connection,omitempty" jsonschema:"also list what this connection can see"`
}

type addFlightIn struct {
	Unit       string `json:"unit" jsonschema:"the Squadron to make the Flight in"`
	Repository string `json:"repository,omitempty" jsonschema:"a hosted repository, OWNER/NAME"`
	Repo       string `json:"repo,omitempty" jsonschema:"the address of another repository"`
	Name       string `json:"name,omitempty" jsonschema:"the Flight's name; by default the repository's"`
	Branch     string `json:"branch,omitempty" jsonschema:"the default branch"`
}

type moveFlightsIn struct {
	Projects []string          `json:"projects" jsonschema:"the Flights to move (a Flight's permanent name is project)"`
	To       string            `json:"to,omitempty" jsonschema:"the Squadron to move to; empty takes them out of any Squadron (Unfiled)"`
	Homes    map[string]string `json:"homes,omitempty" jsonschema:"a Squadron for each Flight, to put each in its own (the undo of a move)"`
	Comment  string            `json:"comment,omitempty" jsonschema:"the reason, a sentence kept with the change"`
}

type organizeUnitsIn struct {
	Groups     []organizeGroupIn `json:"groups,omitempty" jsonschema:"the Squadrons to make or use and the Flights to file in each; without it the proposal is returned and nothing changes"`
	Homes      map[string]string `json:"undo_homes,omitempty" jsonschema:"to undo an organize: each Flight and the Squadron it came from, from the earlier answer"`
	DeleteUnit []string          `json:"undo_delete,omitempty" jsonschema:"to undo an organize: the Squadrons it made, from the earlier answer"`
}

type organizeGroupIn struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name,omitempty"`
	Description string   `json:"description,omitempty"`
	Flights     []string `json:"flights"`
}

type squadronOut struct {
	Result any `json:"result" jsonschema:"what the API answered"`
}

func addSquadronTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "list_units", Annotations: readOnly("List Squadrons"),
		Description: "List the Squadrons (folders of Flights), each with how many Flights it holds and how many need attention, and the Flights that sit in none (Unfiled)."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ struct{}) (squadronOut, error) {
			return squadronCall(ctx, c, "list_units", http.MethodGet, "/api/v1/units", nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "get_unit", Annotations: readOnly("Get a Squadron"),
		Description: "One Squadron: its Flights with their latest result, what needs attention, its people and shared settings (for those who may see them), and its Operations: pipelines nothing starts but a person."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in unitNameIn) (squadronOut, error) {
			return squadronCall(ctx, c, "get_unit", http.MethodGet, "/api/v1/units/"+url.PathEscape(in.Name), nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "create_unit", Annotations: changes("Make a Squadron", false, false),
		Description: "Make a Squadron with a name (and, if given, a display name and description). It starts empty. Needs the admin role on every Flight. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in unitCreateIn) (squadronOut, error) {
			return squadronCall(ctx, c, "create_unit", http.MethodPost, "/api/v1/units", in)
		})
	addTool(srv, c, &mcp.Tool{Name: "list_project_candidates", Annotations: readOnly("Repositories a Flight could be made from"),
		Description: "Search repositories a Flight could be made from: the hosted repositories that have no Flight yet first, then those that have one; name a connection to also list what it can see. Needs the admin role on every Flight."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in candidatesIn) (squadronOut, error) {
			p := "/api/v1/project-candidates?q=" + url.QueryEscape(in.Query)
			if in.Connection != "" {
				p += "&connection=" + url.QueryEscape(in.Connection)
			}
			return squadronCall(ctx, c, "list_project_candidates", http.MethodGet, p, nil)
		})
	addTool(srv, c, &mcp.Tool{Name: "create_project_in_unit", Annotations: changes("Add a Flight to a Squadron", false, false),
		Description: "Make a Flight in a Squadron from a repository: a hosted one by its path, or another by its address. The answer says whether the repository's pipeline file was found, or gives a starter to commit with the pipeline editor. Needs the admin role on every Flight. Audited."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in addFlightIn) (squadronOut, error) {
			return squadronCall(ctx, c, "create_project_in_unit", http.MethodPost, "/api/v1/units/"+url.PathEscape(in.Unit)+"/projects", in)
		})
	addTool(srv, c, &mcp.Tool{Name: "preview_project_move", Annotations: readOnly("Preview moving Flights"),
		Description: "Say, for each Flight, what moving it to another Squadron would change: shared settings that stop or start applying, variables that resolve differently, who gains or loses the ability to start runs, and whether a secret reference would resolve to another store or none (which blocks that Flight's move). Changes nothing."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in moveFlightsIn) (squadronOut, error) {
			return squadronCall(ctx, c, "preview_project_move", http.MethodPost, "/api/v1/unit-moves/preview", in)
		})
	addTool(srv, c, &mcp.Tool{Name: "move_projects", Annotations: changes("Move Flights", false, true),
		Description: "Move Flights to another Squadron in one audited change. Flights whose move is blocked stay where they are and are listed with the reason. The answer holds undo: send it back as homes to put each Flight back. Needs the admin role on every Flight."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in moveFlightsIn) (squadronOut, error) {
			return squadronCall(ctx, c, "move_projects", http.MethodPost, "/api/v1/unit-moves", in)
		})
	addTool(srv, c, &mcp.Tool{Name: "organize_into_units", Annotations: changes("Organize Flights into Squadrons", false, false),
		Description: "For Flights that sit in no Squadron: without groups, propose Squadrons from the folders that exist and from name prefixes (changes nothing). With groups, make the Squadrons that do not exist and file the Flights as one audited change; the answer holds what undoes it (send it back as undo_homes and undo_delete). Needs the admin role on every Flight."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in organizeUnitsIn) (squadronOut, error) {
			switch {
			case len(in.Homes) > 0:
				return squadronCall(ctx, c, "organize_into_units", http.MethodPost, "/api/v1/unit-organize/undo", map[string]any{"homes": in.Homes, "delete_units": in.DeleteUnit})
			case len(in.Groups) > 0:
				return squadronCall(ctx, c, "organize_into_units", http.MethodPost, "/api/v1/unit-organize", map[string]any{"groups": in.Groups})
			}
			return squadronCall(ctx, c, "organize_into_units", http.MethodGet, "/api/v1/unit-organize", nil)
		})
}

func squadronCall(ctx context.Context, c *mcpCall, tool, method, path string, body any) (squadronOut, error) {
	var out any
	if err := c.apiJSON(ctx, tool, method, path, body, &out); err != nil {
		return squadronOut{}, err
	}
	return squadronOut{Result: out}, nil
}
