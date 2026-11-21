package engine

// What schema 44 (the Debrief's statistics) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersDebrief = schemaMarker{
	schema: schemaDebrief, feature: "per-test statistics and flaky digests",
	tables: []string{"test_stats", "test_flake_digests"}, columns: []string{"coverage_files.covered_lines", "coverage_files.missed_lines"},
}
