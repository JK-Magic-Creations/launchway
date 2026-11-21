package engine

// What schema 27 brought, in the shape of an entry of schemaMarkers (schemas
// 25 and 26 are other branches' and stand in as empty migrations until they
// merge).
var schemaMarkersParity2 = schemaMarker{
	schema: schemaParity2, feature: "a runner for a single step and approvals by several people",
	columns: []string{"steps.own_workspace", "steps.runner_pool", "steps.runner_labels", "steps.approvals"},
}
