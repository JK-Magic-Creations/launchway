package engine

// Schema 34 belongs to another branch (encryption at rest) that has not
// merged. Until it does, this build holds an empty stand-in so that the
// number after it keeps the number it will have when all are merged. When the
// real 34 merges, replace this file with it and the stand-in checks in
// migrations_order_test.go with its own.

// schemaStandIn34 is the number held for that branch.
const schemaStandIn34 = 34

func init() {
	if len(migrations) != schemaStandIn34-1 {
		panic("the stand-in for migration 34 must follow migrations 1 to 33")
	}
	migrations = append(migrations, migration{additive, ``})
}
