package engine

// Schema 61 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 61 merges, replace this file with it (and delete
// schemaStandIn61 from the order test's list and its empty-number allowance).

// schemaStandIn61 is the number held for that branch.
const schemaStandIn61 = 61

func init() {
	if len(migrations) != schemaStandIn61-1 {
		panic("the stand-in for migration 61 must follow migrations 1 to 60")
	}
	migrations = append(migrations, migration{additive, ``})
}
