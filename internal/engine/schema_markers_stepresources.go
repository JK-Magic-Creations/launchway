package engine

// What schema 22 (step resources) brought, in the shape of an entry of
// schemaMarkers, which lists what each schema brought so that the migration
// tests and the startup guard can tell a real migration from a placeholder.
//
// TODO: main has no production list of markers or startup guard yet: schemaMarkers
// exists only in migrations_order_test.go. Register this entry in the
// production list as soon as one exists.
//
// It is its own entry, in its own file, because schemaMarkers is being
// rewritten on another branch (fix/schema-guard moves it from
// migrations_order_test.go to schema_markers.go): when the two are merged,
// add schemaMarkersStepResources as the last entry of schemaMarkers there, and
// delete the init in schema_markers_stepresources_test.go, which only
// registers it for the tests until then.
var schemaMarkersStepResources = struct {
	schema  int
	feature string
	tables  []string
	columns []string
}{schemaResources, "step resources", []string{"step_resource_defaults", "step_resource_limits"}, nil}
