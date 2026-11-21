package engine

// What schema 38 (queues and limits) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersQueues = schemaMarker{
	schema: schemaQueues, feature: "queues and limits",
	tables: []string{"run_queue", "step_waits"},
}
