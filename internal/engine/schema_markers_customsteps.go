package engine

// What schema 68 (custom steps) brought, in the shape of an entry of schemaMarkers.
var schemaMarkersCustomSteps = schemaMarker{
	schema: schemaCustomSteps, feature: "custom steps",
	tables: []string{"custom_step_uses"},
}
