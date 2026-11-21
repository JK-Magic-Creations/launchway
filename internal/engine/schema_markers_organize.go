package engine

// What schema 59 (folders, labels and layouts) brought, in the shape of an
// entry of schemaMarkers.
var schemaMarkersOrganize = schemaMarker{
	schema: schemaOrganize, feature: "folders, labels and layouts",
	tables: []string{"org_folders", "org_folder_items", "org_labels", "org_layouts", "org_user_layouts"},
}
