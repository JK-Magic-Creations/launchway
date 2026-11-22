package engine

// What schema 60 (notifications people receive) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersNotices = schemaMarker{
	schema: schemaNotices, feature: "notifications people receive",
	tables: []string{"notices", "notice_prefs", "notice_watch", "notice_people", "mail_queue"},
}
