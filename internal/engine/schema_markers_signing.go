package engine

// What schema 61 (verified commits and tags) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersSigning = schemaMarker{
	schema: schemaSigning, feature: "verified commits and tags",
	tables: []string{"signing_keys", "signature_cache", "signing_installation"},
}
