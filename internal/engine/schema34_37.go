package engine

// Schemas 34 to 37 belong to other branches that have not merged (a release
// integration, an installer). Until they do, this build holds empty stand-ins
// so that the number after them, 38 (queues and limits, schema38.go), is the
// number it will have when all are merged. When a real migration merges,
// replace its stand-in here with it; the migration-order tests allow exactly
// these stand-ins.

// The numbers held for those branches.
const (
	schemaStandIn34 = 34
	schemaStandIn37 = 37
)

func init() {
	if len(migrations) != schemaStandIn34-1 {
		panic("the stand-ins for migrations 34 to 37 must follow migrations 1 to 33")
	}
	for n := schemaStandIn34; n <= schemaStandIn37; n++ {
		migrations = append(migrations, migration{additive, ``})
	}
}
