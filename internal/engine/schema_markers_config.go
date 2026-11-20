package engine

// What schema 23 (configuration in the database) brought, in the shape of an
// entry of schemaMarkers (see schema_markers_stepresources.go for why it is
// its own variable: when fix/schema-guard is merged, add it as the last
// entry of schemaMarkers in schema_markers.go and delete the init in
// schema_markers_config_test.go).
var schemaMarkersConfig = struct {
	schema  int
	feature string
	tables  []string
	columns []string
}{schemaConfig, "configuration in the database", []string{"config_objects", "config_changes", "config_imports", "config_revisions"}, nil}
