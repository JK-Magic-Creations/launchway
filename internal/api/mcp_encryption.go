package api

// The MCP tool for encryption at rest: read-only, for administrators, the
// same report as GET /api/v1/encryption.

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/sealing"
)

func addEncryptionTools(srv *mcp.Server, c *mcpCall) {
	if !c.mayAdminister {
		return
	}
	addTool(srv, c, &mcp.Tool{Name: "get_encryption_status", Annotations: readOnly("Get encryption at rest status"),
		Description: "Administrators only. Whether the installation's stored secrets are encrypted at rest: which holder keeps the root key (key file, environment, passphrase keystore or Vault/OpenBao transit; the key itself is never shown), the key versions, how many secrets are sealed and how many remain in the clear, the progress of sealing existing secrets, warnings, and whether a copy of the database alone could reveal a secret. Says plainly what is not yet encrypted (artifacts, logs, caches, Git packs). Changing keys is done on the server host with launchway encryption, not through this tool."},
		mcpCallTimeout, getEncryptionStatus)
}

type getEncryptionStatusIn struct{}

func getEncryptionStatus(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, _ getEncryptionStatusIn) (sealing.Report, error) {
	var out sealing.Report
	err := c.apiJSON(ctx, "get_encryption_status", http.MethodGet, "/api/v1/encryption", nil, &out)
	return out, err
}
