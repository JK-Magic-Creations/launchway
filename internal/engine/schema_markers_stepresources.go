package engine

// What schema 22 (step resources) brought, in the shape of an entry of
// schemaMarkers, which lists what each schema brought so that the migration
// tests and the startup guard can tell a real migration from a placeholder.
// It is its own entry, in its own file, and is the last entry of
// schemaMarkers.
var schemaMarkersStepResources = schemaMarker{
	schema: schemaResources, feature: "step resources",
	tables: []string{"step_resource_defaults", "step_resource_limits"},
}
