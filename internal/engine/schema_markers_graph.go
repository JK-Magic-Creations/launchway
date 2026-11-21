package engine

// What schema 47 (the software graph) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersGraph = schemaMarker{
	schema: schemaGraph, feature: "the software graph",
	tables: []string{"graph_nodes", "graph_edges", "graph_overrides", "graph_runs"},
}
