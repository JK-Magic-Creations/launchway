package engine

// What schema 43 (review depth) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersReview = schemaMarker{
	schema: schemaReview, feature: "code owners, queue and viewed files for pull requests",
	tables:  []string{"pull_viewed", "pull_queue"},
	columns: []string{"branch_rules.extra", "pull_reviews.reviewer_groups", "repo_pull_settings.extra"},
}
