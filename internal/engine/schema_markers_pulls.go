package engine

// What schema 33 (pull requests) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersPulls = schemaMarker{
	schema: schemaPulls, feature: "pull requests and code review",
	tables: []string{"pull_counters", "pull_requests", "pull_people", "pull_revisions", "pull_runs", "pull_reviews", "pull_comments", "repo_pull_settings"},
	columns: []string{"branch_rules.required_approvals", "branch_rules.approval_role", "branch_rules.required_checks", "branch_rules.require_up_to_date",
		"branch_rules.require_resolved", "branch_rules.no_self_approval", "branch_rules.require_signed", "branch_rules.bypass_admins"},
}
