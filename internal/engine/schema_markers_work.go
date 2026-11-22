package engine

// What schema 50 (work tracking) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersWork = schemaMarker{
	schema: schemaWork, feature: "work tracking",
	tables: []string{"work_scopes", "work_items", "work_labels", "work_assignees", "work_watchers", "work_links", "work_code_links",
		"work_comments", "work_comment_edits", "work_events", "work_criteria", "work_milestones", "work_sprints", "work_sprint_items",
		"work_attachments", "work_views", "work_agents", "work_claims", "work_inbox", "work_agent_log", "work_usage", "work_agent_changes", "work_autonomy_state", "work_agent_reviews", "work_review_samples", "work_proposals"},
}
