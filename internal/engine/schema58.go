package engine

// Schema 58 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 58 merges, replace this file with it (and delete
// schemaStandIn58 from the order test's list and its empty-number allowance).

// schemaStandIn58 is the number held for that branch.
const schemaStandIn58 = 58

func init() {
	if len(migrations) != schemaStandIn58-1 {
		panic("the stand-in for migration 58 must follow migrations 1 to 57")
	}
	migrations = append(migrations, migration{additive, ``})
}
