package engine

// What schema 42 (the event log) brought, in the shape of an entry of
// schemaMarkers.
var schemaMarkersEvents = schemaMarker{
	schema: schemaEvents, feature: "the event log and event sources",
	tables: []string{"event_log", "event_decisions", "event_sources", "event_dedupe", "event_windows", "event_cursors", "run_triggers", "run_trigger_allow"},
}
