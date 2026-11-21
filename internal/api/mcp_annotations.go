package api

import "github.com/modelcontextprotocol/go-sdk/mcp"

func readOnly(title string) *mcp.ToolAnnotations {
	no := false
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &no}
}

// changes marks a tool that changes something. Runs and rollouts reach
// systems outside Launchway, so their world is open.
func changes(title string, destructive, idempotent bool) *mcp.ToolAnnotations {
	yes := true
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: &destructive, IdempotentHint: idempotent, OpenWorldHint: &yes}
}
