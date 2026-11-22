package engine

// What schema 56 (the release published to an outside Git host) brought, in
// the shape of an entry of schemaMarkers.
var schemaMarkersOutside = schemaMarker{
	schema: schemaOutside, feature: "release published to an outside Git host",
	tables:  []string{"release_outside"},
	columns: []string{"repo_mirrors.publish_releases", "repo_mirrors.outside_api"},
}
