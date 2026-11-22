package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addInsightsTools registers the Insights tool: the same numbers as the Insights pages, for the Flights the caller may read.
func addInsightsTools(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "get_insights", Annotations: readOnly("Get Insights"),
		Description: "How delivery is going over a period, for one Flight, one Squadron or every Flight you may see: run success rate and duration (median and 95th percentile) over time, queue wait, the slowest and flakiest steps, test totals and failures, deploy frequency, lead time from merge to deploy, change failure rate and time to restore. Counts only Flights you may read."},
		mcpCallTimeout, func(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in struct {
			Flight   string `json:"flight,omitempty" jsonschema:"one Flight's name"`
			Squadron string `json:"squadron,omitempty" jsonschema:"one Squadron's name (not together with flight)"`
			From     string `json:"from,omitempty" jsonschema:"start of the period (default 30 days before the end)"`
			To       string `json:"to,omitempty" jsonschema:"end of the period (default now)"`
			Branch   string `json:"branch,omitempty" jsonschema:"count only runs of this branch"`
			Bucket   string `json:"bucket,omitempty" jsonschema:"hour, day, week or month (default chosen from the period)"`
		}) (map[string]any, error) {
			q := url.Values{"flight": {in.Flight}, "squadron": {in.Squadron}, "from": {in.From}, "to": {in.To}, "branch": {in.Branch}, "bucket": {in.Bucket}}
			out := map[string]any{}
			err := c.apiJSON(ctx, "get_insights", http.MethodGet, "/api/v1/insights?"+q.Encode(), nil, &out)
			return out, err
		})
}
