package engine

// What schema 45 (the archive of another server's issues) brought, in the
// shape of an entry of schemaMarkers.
var schemaMarkersArchive = schemaMarker{
	schema: schemaArchive, feature: "imported issues archive",
	tables: []string{"imported_issues", "imported_comments"},
}
