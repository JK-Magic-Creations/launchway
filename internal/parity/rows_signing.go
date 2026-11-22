package parity

// The rows of verified commits and tags (internal/forge/signing), in a file
// of their own.

func init() {
	const tf = "not needed by terraform"
	rows = append(rows,
		Row{Route: "GET /api/v1/user/signing-keys", MCP: "list_signing_keys", CLI: "signing keys list", Terraform: tf, Status: Equal},
		Row{Route: "POST /api/v1/user/signing-keys", MCP: "add_signing_key", CLI: "signing keys add", Terraform: tf, Status: Equal},
		Row{Route: "DELETE /api/v1/user/signing-keys/{id}", MCP: "delete_signing_key", CLI: "signing keys delete", Terraform: tf, Status: Equal},
		Row{Route: "POST /api/v1/user/signing-keys/{id}/revoke", MCP: "revoke_signing_key", CLI: "signing keys revoke", Terraform: tf, Status: Equal},
		Row{Route: "GET /api/v1/repositories/{owner}/{name}/signatures", MCP: "verify_signatures", CLI: "signing verify", Terraform: tf, Status: Equal},
		Row{Route: "GET /api/v1/signing/installation-key", MCP: "get_signing_installation_key", CLI: "signing installation-key", Terraform: tf, Status: Equal},
		Row{Route: "GET /.well-known/launchway/signing-key.pub", MCP: "get_signing_installation_key", CLI: "signing installation-key", Status: Exempt,
			Reason: "the same public key as a plain file at a fixed address, for allowed-signers files and anyone verifying without signing in"},
	)
}
