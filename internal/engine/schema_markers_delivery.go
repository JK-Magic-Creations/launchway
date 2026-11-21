package engine

// What schema 39 (delivery) brought, in the shape of an entry of schemaMarkers.
var schemaMarkersDelivery = schemaMarker{
	schema: schemaDelivery, feature: "pipeline identities on protected branches",
	tables: []string{"branch_writers"},
}
