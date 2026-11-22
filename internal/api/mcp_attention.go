package api

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

type attentionIn struct {
	IncludeDismissed bool `json:"include_dismissed,omitempty" jsonschema:"also list what the token's person set aside"`
}

type attentionOut struct {
	Needs      []engine.AttentionItem `json:"needs" jsonschema:"what needs a person, most urgent first; each says why it is listed and what to do"`
	Retrying   []engine.Retry         `json:"retrying" jsonschema:"failures that a newer Sortie of the same pipeline and branch is retrying; they need nobody"`
	Dismissed  []engine.AttentionItem `json:"dismissed,omitempty"`
	Count      int                    `json:"count"`
	Superseded int                    `json:"superseded" jsonschema:"how many failures were set aside because something newer replaced them"`
}

func addAttentionTool(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "attention", Annotations: readOnly("What needs attention"),
		Description: "What needs a person now, by the same rule as the Tower: a failed Sortie only when nothing newer is running, waiting to start or has succeeded on its branch or pull request and it is on a branch that matters; a failed deployment not followed by a success or a return; decisions that wait; Sorties that are stuck. Each entry says why and what to do. Use this before list_runs to find what to look at."},
		mcpCallTimeout, attention)
}

func attention(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in attentionIn) (attentionOut, error) {
	var a attentionOut
	if err := c.apiJSON(ctx, "attention", http.MethodGet, "/api/v1/attention", nil, &a); err != nil {
		return attentionOut{}, err
	}
	if !in.IncludeDismissed {
		a.Dismissed = nil
	}
	return a, nil
}
