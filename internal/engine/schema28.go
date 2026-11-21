package engine

// Schema 28 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the number after it, 29 (variables
// at levels, schema29.go), is the number it will have when both are merged.
// The migration-order tests allow exactly this one stand-in; when the real
// 28 merges, replace this file with it.

// schemaStandIn28 is the number held for that branch.
const schemaStandIn28 = 28

func init() {
	if len(migrations) != schemaStandIn28-1 {
		panic("the stand-in for migration 28 must follow migrations 1 to 27")
	}
	migrations = append(migrations, migration{additive, ``})
}
