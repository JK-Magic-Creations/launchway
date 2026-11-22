package api

// MCP tools of the hosting and release gaps closed together (see parity_routes.go). Each calls the route of the
// API, so the answer for a caller is the API's.

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func addParityTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_hosting_settings", Annotations: readOnly("Get the hosting settings"),
		Description: "The installation's hosting settings: the most a snapshot (tar.gz or zip) of a repository's ref may hold, in MiB, and how many days a deleted repository stays in the trash before it is removed for good."},
		mcpCallTimeout, getHostingSettings)
	addTool(srv, c, &mcp.Tool{Name: "get_flight_badges", Annotations: readOnly("Get a Flight's badges"),
		Description: "Whether a Flight's status and release badges are shown to anyone (switched on, or its repository is public), their addresses and the Markdown to paste. A badge holds only a status word or a version."},
		mcpCallTimeout, getFlightBadges)
	if c.mayOperate {
		addTool(srv, c, &mcp.Tool{Name: "set_flight_badges", Annotations: changes("Switch a Flight's badges on or off", false, true),
			Description: "Administrators of the Flight only. Show the Flight's badges to anyone, or hide them again (off is the default for a Flight of a private repository). Audited."},
			mcpCallTimeout, setFlightBadges)
	}
	if c.mayAdminister {
		addTool(srv, c, &mcp.Tool{Name: "set_hosting_settings", Annotations: changes("Change the hosting settings", false, true),
			Description: "Installation administrators only. Change the largest snapshot (MiB) and how many days a deleted repository is kept; leave a field out to keep it. Audited."},
			mcpCallTimeout, setHostingSettings)
	}
}

type hostingSettingsOut struct {
	ArchiveMaxMiB      int `json:"archive_max_mib"`
	TrashRetentionDays int `json:"trash_retention_days"`
}

func getHostingSettings(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ struct{}) (hostingSettingsOut, error) {
	var out hostingSettingsOut
	err := c.apiJSON(ctx, "get_hosting_settings", http.MethodGet, "/api/v1/hosting-settings", nil, &out)
	return out, err
}

type setHostingSettingsIn struct {
	ArchiveMaxMiB      *int `json:"archive_max_mib,omitempty" jsonschema:"the most a snapshot of a ref may hold, in MiB of files (1 to 65536)"`
	TrashRetentionDays *int `json:"trash_retention_days,omitempty" jsonschema:"how many days a deleted repository can be restored (1 to 3650)"`
}

func setHostingSettings(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setHostingSettingsIn) (hostingSettingsOut, error) {
	var out hostingSettingsOut
	err := c.apiJSON(ctx, "set_hosting_settings", http.MethodPut, "/api/v1/hosting-settings", in, &out)
	return out, err
}

type flightBadgesOut struct {
	Enabled          bool              `json:"enabled"`
	PublicRepository bool              `json:"public_repository"`
	Shown            bool              `json:"shown"`
	Branch           string            `json:"branch"`
	Links            map[string]string `json:"links"`
	Markdown         map[string]string `json:"markdown"`
}

type flightBadgesIn struct {
	Project string `json:"project" jsonschema:"the Flight's name"`
}

func getFlightBadges(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in flightBadgesIn) (flightBadgesOut, error) {
	var out flightBadgesOut
	err := c.apiJSON(ctx, "get_flight_badges", http.MethodGet, apiPath("/api/v1/projects/%s/badges", in.Project), nil, &out)
	return out, err
}

type setFlightBadgesIn struct {
	Project string `json:"project" jsonschema:"the Flight's name"`
	Enabled bool   `json:"enabled" jsonschema:"true shows the badges to anyone, false hides them"`
}

func setFlightBadges(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in setFlightBadgesIn) (flightBadgesOut, error) {
	var out flightBadgesOut
	err := c.apiJSON(ctx, "set_flight_badges", http.MethodPut, apiPath("/api/v1/projects/%s/badges", in.Project), map[string]any{"enabled": in.Enabled}, &out)
	return out, err
}
