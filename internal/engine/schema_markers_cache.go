package engine

// What schema 25 (build caches) brought, in the shape of an entry of
// schemaMarkers, which lists what each schema brought so that the migration
// tests and the startup guard can tell a real migration from a placeholder.
// It is its own entry, in its own file, so that the other schemas' markers
// do not collide with it when branches merge. Schemas 23 and 24 are other
// branches' and have no marker here.
var schemaMarkersCache = schemaMarker{
	schema: schemaCache, feature: "build caches",
	tables: []string{"caches", "cache_usage", "cache_policy"},
}
