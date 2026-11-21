package engine

// What schema 57 (releases) brought, in the shape of an entry of schemaMarkers.
var schemaMarkersReleases = schemaMarker{
	schema: schemaReleases, feature: "releases and release management",
	tables: []string{"release_settings", "releases", "release_assets", "release_gates", "release_clearances", "release_promotions",
		"release_freezes", "release_formations", "release_formation_members"},
}
