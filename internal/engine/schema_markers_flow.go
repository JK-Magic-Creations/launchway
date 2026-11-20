package engine

// What schema 24 (flow control) brought, in the shape of an entry of
// schemaMarkers. Schema 23 (the configuration kept in the database) was
// built elsewhere and has its own entry where it merges; until then the
// number is a stand-in, which the migration tests allow.
var schemaMarkersFlow = schemaMarker{
	schema: schemaFlow, feature: "flow control",
	tables:  []string{"installation_limits", "lane_holders", "lane_waiting"},
	columns: []string{"steps.allowed", "steps.not_before", "steps.fail_cause", "runs.deadline_at", "runs.lane_name", "runs.lane_key", "runs.lane_label", "runs.lane_state", "runs.lane_other", "runs.lane_other_number"},
}
