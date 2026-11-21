package engine

// Schemas 34 to 38 belong to branches that have not merged (encryption at
// rest, the build cache, built-in accounts, the installer, CI robustness).
// Until they do, this build holds empty stand-ins so that 39 (delivery:
// hosted write-back and notifications, schema39.go) is the number it will
// have when all are merged. When a real one merges, replace its stand-in
// with it and set the stand-in aside in the order test.

// schemaStandIn34 to schemaStandIn38 are the numbers held for those branches.
const (
	schemaStandIn34 = 34
	schemaStandIn35 = 35
	schemaStandIn36 = 36
	schemaStandIn37 = 37
	schemaStandIn38 = 38
)

func init() {
	if len(migrations) != schemaStandIn34-1 {
		panic("the stand-ins for migrations 34 to 38 must follow migrations 1 to 33")
	}
	for i := schemaStandIn34; i <= schemaStandIn38; i++ {
		migrations = append(migrations, migration{additive, ``})
	}
}
