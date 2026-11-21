package engine

// What schema 41 (feature toggles) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersToggles = schemaMarker{
	schema: schemaToggles, feature: "feature toggles",
	tables: []string{"toggles", "toggle_envs", "toggle_segments", "toggle_changes", "toggle_keys", "toggle_schedules", "toggle_safeguards", "toggle_evals", "toggle_revs"},
}
