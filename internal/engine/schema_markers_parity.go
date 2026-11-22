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

var schemaMarkersOverview = schemaMarker{
	schema: schemaOverview, feature: "repository Overview: languages and contributors",
	tables: []string{"repo_languages", "repo_contributors"},
}

var schemaMarkersWorkExtras = schemaMarker{
	schema: schemaWorkExtras, feature: "reactions, time entries, deployment addresses and decision records on work",
	tables: []string{"work_reactions", "work_time_entries", "work_decisions"}, columns: []string{"deployments.address"},
}
