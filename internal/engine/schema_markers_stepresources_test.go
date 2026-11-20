package engine

// Registers schema 22's markers with the migration tests until they are in
// schemaMarkers itself (see schema_markers_stepresources.go).
func init() { schemaMarkers = append(schemaMarkers, schemaMarkersStepResources) }
