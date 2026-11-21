package engine

// What schema 48 (configuration as code) brought, in the shape of an entry
// of schemaMarkers.
var schemaMarkersCac = schemaMarker{
	schema: schemaConfigAsCode, feature: "configuration as code",
	tables: []string{"cac_reports", "tf_workspaces", "tf_state_versions", "tf_locks"},
}
