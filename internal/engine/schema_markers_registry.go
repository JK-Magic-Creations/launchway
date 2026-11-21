package engine

// What schema 40 (the built-in registry) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersRegistry = schemaMarker{
	schema: schemaRegistry, feature: "the built-in registry",
	tables: []string{"registry_repositories", "registry_blobs", "registry_repo_blobs", "registry_manifests", "registry_manifest_refs",
		"registry_tags", "registry_uploads", "registry_upload_chunks", "registry_policies", "registry_gc_state"},
}
