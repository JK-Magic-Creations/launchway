package engine

// What schema 55 (Standing orders) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersStandings = schemaMarker{
	schema: schemaStandings, feature: "standing orders",
	tables: []string{"standing_settings", "standing_tracked", "standing_decisions", "standing_verdicts", "standing_history"},
	columns: []string{"findings.check_id", "findings.kind", "findings.package", "findings.version", "findings.fixed_in", "findings.advisories",
		"findings.reachable", "findings.fix", "findings.track"},
}
