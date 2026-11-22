package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type reactIn struct {
	Item     string `json:"item,omitempty" jsonschema:"the item's reference, such as KEY-12 (to react to the item)"`
	Comment  string `json:"comment,omitempty" jsonschema:"a comment's id (to react to the comment; leave item out)"`
	Reaction string `json:"reaction" jsonschema:"thumbs_up, thumbs_down, laugh, hooray, confused, heart, rocket or eyes"`
	Remove   bool   `json:"remove,omitempty" jsonschema:"take your reaction back instead of making it"`
}

// addWorkReactTool offers the reaction tool to everyone who can read work: reacting needs only that you can see the
// item, as in the API.
func addWorkReactTool(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "react_to_work", Annotations: changes("React to a work item or comment", false, true),
		Description: "Make (or with remove, take back) your reaction to a work item or to one of its comments, from a fixed set. One reaction of each kind per person; the answer has the counts and which are yours. Anyone who can see the item may react; an agent may not. Audited."},
		mcpCallTimeout, reactToWork)
}

func reactToWork(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in reactIn) (workOut, error) {
	if (in.Item == "") == (in.Comment == "") || in.Reaction == "" {
		return nil, errors.New("say the item or the comment, and the reaction")
	}
	method := http.MethodPut
	if in.Remove {
		method = http.MethodDelete
	}
	p := apiPath("/api/v1/work/items/%s/reactions/%s", in.Item, in.Reaction)
	if in.Comment != "" {
		p = apiPath("/api/v1/work/comments/%s/reactions/%s", in.Comment, in.Reaction)
	}
	return workCall(ctx, c, "react_to_work", method, p, nil)
}
