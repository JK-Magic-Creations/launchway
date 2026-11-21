package engine

// What schema 32 brought, in the shape of an entry of schemaMarkers.
var schemaMarkersRollback = schemaMarker{
	schema: schemaRollback, feature: "rollback after a successful deployment",
	tables: []string{"rollbacks", "rollback_reverts"},
}
