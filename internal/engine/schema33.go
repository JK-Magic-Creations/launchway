package engine

// Schema 33 belongs to another branch (the integration of pull requests and
// the forge pages) that has not merged. Until it does, this build holds an
// empty stand-in so that the number after it keeps the number it will have
// when all are merged. When the real 33 merges, replace this file with it and
// the stand-in checks in migrations_order_test.go with its own.

// schemaStandIn33 is the number held for that branch.
const schemaStandIn33 = 33

func init() {
	if len(migrations) != schemaStandIn33-1 {
		panic("the stand-in for migration 33 must follow migrations 1 to 32")
	}
	migrations = append(migrations, migration{additive, ``})
}
