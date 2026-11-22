package engine

// Schema 68 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 68 merges, replace this file with it (and delete
// schemaStandIn68 from the order test's list and its empty-number allowance).

// schemaStandIn68 is the number held for that branch.
const schemaStandIn68 = 68

func init() {
	if len(migrations) != schemaStandIn68-1 {
		panic("the stand-in for migration 68 must follow migrations 1 to 67")
	}
	migrations = append(migrations, migration{additive, ``})
}
