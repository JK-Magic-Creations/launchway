package engine

// What schemas 64 and 65 (forks, templates, pull requests from forks) brought,
// in the shape of entries of schemaMarkers.
var schemaMarkersForks = schemaMarker{
	schema: schemaForks, feature: "forks and template repositories",
	tables: []string{"owner_fork_settings"},
	columns: []string{"forge_repositories.parent_id", "forge_repositories.root_id", "forge_repositories.fork_point",
		"forge_repositories.forked_by", "forge_repositories.forked_at", "forge_repositories.allow_forks", "forge_repositories.is_template"},
}

var schemaMarkersForkPulls = schemaMarker{
	schema: schemaForkPulls, feature: "pull requests from forks",
	columns: []string{"pull_requests.allow_maintainer_edits", "pull_requests.run_approved_head"},
}
