package engine

// What schema 57 (the outside-host connection) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersHosts = schemaMarker{
	schema: schemaHosts, feature: "connection to an outside Git host",
	tables: []string{"outside_flights", "outside_comments"},
	columns: []string{"forge_connections.auth_kind", "forge_connections.app_ref", "forge_connections.owners",
		"forge_connections.webhook_env", "forge_connections.webhook_file", "forge_connections.upload_url"},
}
