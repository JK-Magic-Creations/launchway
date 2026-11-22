package engine

// What schema 62 (the sizing of a step attempt) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersSizing = schemaMarker{
	schema: schemaSizing, feature: "step sizing columns",
	columns: []string{"step_usage.sizing", "step_usage.sizing_runs", "step_usage.killed"},
}
