package engine

// What schema 70 (Bases, accounts, memberships) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersWall = schemaMarker{
	schema: schemaWall, feature: "Bases, accounts and memberships",
	tables: []string{"bases", "accounts", "memberships"},
	columns: []string{"users.base_id", "sessions.base_id", "api_tokens.base_id", "local_accounts.base_id",
		"local_links.base_id", "local_recovery_codes.base_id"},
}
