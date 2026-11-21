package parity

// The rows of the built-in registry's management routes, in a file of their
// own beside rows.go.

const registryPolicyReason = "MCP and CLI manage the registry policy; it is held in a table of its own, so Terraform cannot yet (backlog: make it a configuration kind)"

func init() {
	rows = append(rows,
		Row{Route: "GET /api/v1/registry", MCP: "registry_status", CLI: "registry status", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "GET /api/v1/registry/gc", MCP: "registry_gc", CLI: "registry gc", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "POST /api/v1/registry/gc", MCP: "registry_gc", CLI: "registry gc", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "GET /api/v1/registry/policy", MCP: "get_registry_policy", CLI: "registry policy get", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "PUT /api/v1/registry/policy", MCP: "set_registry_policy", CLI: "registry policy set", Status: Exempt, Reason: registryPolicyReason},
		Row{Route: "GET /api/v1/projects/{name}/registry", MCP: "list_registry_images", CLI: "registry list", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "GET /api/v1/projects/{name}/registry/images", MCP: "list_registry_images", CLI: "registry list, registry tags", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "GET /api/v1/projects/{name}/registry/image", MCP: "inspect_registry_image", CLI: "registry inspect", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "DELETE /api/v1/projects/{name}/registry/image", MCP: "delete_registry_image", CLI: "registry delete", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "DELETE /api/v1/projects/{name}/registry/tag", MCP: "delete_registry_image", CLI: "registry delete", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "GET /api/v1/projects/{name}/registry/policy", MCP: "get_registry_policy", CLI: "registry policy get", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "PUT /api/v1/projects/{name}/registry/policy", MCP: "set_registry_policy", CLI: "registry policy set", Status: Exempt, Reason: registryPolicyReason},
		Row{Route: "GET /api/v1/projects/{name}/registry/retention", MCP: "preview_registry_retention", CLI: "registry retention", Terraform: "not needed by terraform", Status: Equal},
		Row{Route: "POST /api/v1/projects/{name}/registry/retention", MCP: "run_registry_retention", CLI: "registry retention", Terraform: "not needed by terraform", Status: Equal},
	)
}
