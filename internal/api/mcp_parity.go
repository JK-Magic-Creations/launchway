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
