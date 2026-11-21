package engine

// What schema 34 brought, in the shape of an entry of schemaMarkers.
var schemaMarkersEncryption = schemaMarker{
	schema: schemaEncryption, feature: "encryption at rest",
	tables: []string{"encryption_state", "encryption_keks", "encryption_deks"},
}
