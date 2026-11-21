package api

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/forge/mirror"
)

func addVerifyRepositoriesTool(srv *mcp.Server, c *mcpCall) {
	addTool(srv, c, &mcp.Tool{Name: "verify_repositories", Annotations: readOnly("Verify repositories against their source"),
		Description: "Administrators only. Prove that every repository of the source exists here with exactly the branches, tags and notes at exactly the commits the source has. Returns, per repository, equal, different (naming the refs), missing or error, and equal true only when all are equal. Run it before a cut-over. Reads only; audited."},
		mirrorSyncTimeout, verifyRepositories)
}

func verifyRepositories(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in sourceIn) (mirror.VerifyResult, error) {
	var out mirror.VerifyResult
	err := c.apiJSON(ctx, "verify_repositories", http.MethodPost, "/api/v1/imports/verify", in, &out)
	return out, err
}
