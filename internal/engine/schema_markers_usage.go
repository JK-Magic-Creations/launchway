package engine

// What schema 54 (what each step attempt used) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersUsage = schemaMarker{
	schema: schemaUsage, feature: "measured step use",
	tables: []string{"step_usage"},
}
