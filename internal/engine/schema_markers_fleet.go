package engine

// What schema 49 (fleet inventories) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersFleet = schemaMarker{
	schema: schemaFleet, feature: "fleet inventory of an Airfield",
	tables: []string{"fleet_hosts"},
}
