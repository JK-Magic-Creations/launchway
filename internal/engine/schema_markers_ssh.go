package engine

// What schema 31 brought, in the shape of an entry of schemaMarkers.
var schemaMarkersSSH = schemaMarker{
	schema: schemaSSH, feature: "Git over SSH, webhooks and branch rules",
	tables: []string{"ssh_host_keys", "ssh_keys", "repo_webhooks", "repo_hook_deliveries", "branch_rules"},
}
