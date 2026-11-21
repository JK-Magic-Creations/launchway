package engine

// What schema 30 (repository mirrors) brought, in the shape of an entry of
// schemaMarkers. It is its own entry, in its own file, after the forge's.
var schemaMarkersMirror = schemaMarker{
	schema: schemaMirror, feature: "mirrors of hosted repositories",
	tables: []string{"repo_mirrors"},
}
