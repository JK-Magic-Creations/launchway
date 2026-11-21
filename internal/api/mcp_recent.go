package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type recentIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many entries, at most 50; default 15"`
}

type recentOut struct {
	Continue []activityView `json:"continue" jsonschema:"what the person started or changed lately and the last things they looked at, with what changed since"`
	Recent   []activityView `json:"recent" jsonschema:"their trail, newest first"`
	Paused   bool           `json:"paused"`
}

func addRecentTool(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "recent", Annotations: readOnly("What I was working on"),
		Description: "What the person this token stands for opened and did lately (Sorties, pipelines, repositories, pull requests, settings pages), with the exact address of each and, for a Sortie, what changed since they were there. It is that person's own trail only, never another user's, and leaves out anything they can no longer see."},
		mcpCallTimeout, recent)
}

func recent(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in recentIn) (recentOut, error) {
	var out recentOut
	n := limit(in.Limit, 15, 50)
	if err := c.apiJSON(ctx, "recent", http.MethodGet, "/api/v1/activity?limit="+strconv.Itoa(n), nil, &out); err != nil {
		return recentOut{}, err
	}
	return out, nil
}
