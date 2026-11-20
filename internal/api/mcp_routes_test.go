package api

// Rows of the permission table for the MCP endpoint. TestPermissionMatrix
// turns it on. A browser's session is never accepted there, only a bearer
// token (see TestMCPTransportRefusals), so everyone in the matrix, who
// calls with a session cookie, is answered 401.

func init() {
	for k, v := range map[string]matrixRow{
		"POST /mcp":   {"POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, [6]int{401, 401, 401, 401, 401, 401}},
		"GET /mcp":    {"GET", "/mcp", "", [6]int{405, 405, 405, 405, 405, 405}},
		"DELETE /mcp": {"DELETE", "/mcp", "", [6]int{405, 405, 405, 405, 405, 405}},
		// Only with mcp.oauth set.
		"GET /.well-known/oauth-protected-resource/mcp": {"GET", "/.well-known/oauth-protected-resource/mcp", "", [6]int{404, 404, 404, 404, 404, 404}},
	} {
		permissionTable[k] = v
	}
	publicRoutes = append(publicRoutes, "POST /mcp", "GET /mcp", "DELETE /mcp", "GET /.well-known/oauth-protected-resource/mcp")
}
