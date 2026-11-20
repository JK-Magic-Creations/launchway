package engine

// Registers schema 23's markers with the migration tests until they are in
// schemaMarkers itself (see schema_markers_config.go).
func init() { schemaMarkers = append(schemaMarkers, schemaMarkersConfig) }
