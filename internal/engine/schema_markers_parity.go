package engine

// What schemas 58 and 59 brought, in the shape of entries of schemaMarkers.
var schemaMarkersTagRules = schemaMarker{
	schema: schemaTagRules, feature: "protected tags and release download counts",
	tables: []string{"tag_rules", "release_downloads"},
}

var schemaMarkersBadges = schemaMarker{
	schema: schemaBadges, feature: "Flight badges, old repository addresses, hosting settings",
	tables: []string{"flight_badges", "repo_aliases", "hosting_settings"},
}

var schemaMarkersTrash = schemaMarker{
	schema: schemaTrash, feature: "who deleted a repository and when, and permanent removal",
	tables: []string{"repo_trash"},
}
