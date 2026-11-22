package engine

// What schema 55 (narrow credentials) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersNarrow = schemaMarker{
	schema: schemaNarrow, feature: "repository and address limits on tokens, service accounts",
	tables:  []string{"service_accounts"},
	columns: []string{"api_tokens.repos", "api_tokens.repo_access", "api_tokens.source_ranges", "audit.token_id", "audit.token_name"},
}
