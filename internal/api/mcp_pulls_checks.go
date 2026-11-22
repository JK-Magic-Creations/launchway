package api

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func getPullChecks(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in repoNumberIn) (pullChecksView, error) {
	p, err := in.path("/checks")
	if err != nil {
		return pullChecksView{}, err
	}
	var out pullChecksView
	if err := c.apiJSON(ctx, "get_pull_checks", http.MethodGet, p, nil, &out); err != nil {
		return out, err
	}
	out.Checks, out.Deployments, out.Notes = orEmpty(out.Checks), orEmpty(out.Deployments), orEmpty(out.Notes)
	return out, nil
}
