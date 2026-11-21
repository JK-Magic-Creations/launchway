package engine

// What schema 60 (inspect requests) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersInspect = schemaMarker{
	schema: schemaInspect, feature: "inspect requests",
	tables: []string{"inspect_requests", "inspect_chunks"},
}
