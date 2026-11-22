package engine

// Schema 62 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 62 merges, replace this file with it (and delete
// schemaStandIn62 from the order test's list and its empty-number allowance).

// schemaStandIn62 is the number held for that branch.
const schemaStandIn62 = 62

func init() {
	if len(migrations) != schemaStandIn62-1 {
		panic("the stand-in for migration 62 must follow migrations 1 to 61")
	}
	migrations = append(migrations, migration{additive, ``})
}
