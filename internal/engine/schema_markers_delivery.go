package engine

// What schema 38 (delivery) brought, in the shape of an entry of schemaMarkers.
var schemaMarkersDelivery = schemaMarker{
	schema: schemaDelivery, feature: "pipeline identities on protected branches and notifications",
	tables: []string{"branch_writers", "notifications", "notify_state"},
}
