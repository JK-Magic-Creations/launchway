package engine

// What schema 26 (forges) brought, in the shape of an entry of schemaMarkers.
// It is its own entry, in its own file, and the last entry of schemaMarkers.
var schemaMarkersForge = schemaMarker{
	schema: schemaForge, feature: "forge connections and repositories",
	tables: []string{"forge_connections", "forge_repositories", "forge_events", "forge_deliveries",
		"repo_generation", "repo_refs", "repo_packs", "repo_maintenance"},
}
