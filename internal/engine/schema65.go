package engine

// Schema 65 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 65 merges, replace this file with it (and delete
// schemaStandIn65 from the order test's list and its empty-number allowance).

// schemaStandIn65 is the number held for that branch.
const schemaStandIn65 = 65

func init() {
	if len(migrations) != schemaStandIn65-1 {
		panic("the stand-in for migration 65 must follow migrations 1 to 64")
	}
	migrations = append(migrations, migration{additive, ``})
}
