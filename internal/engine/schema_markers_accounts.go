package engine

// What schema 36 (built-in accounts) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersAccounts = schemaMarker{
	schema: schemaAccounts, feature: "built-in accounts",
	tables: []string{"local_accounts", "local_recovery_codes", "local_links", "setup_state"},
}
