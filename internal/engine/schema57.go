package engine

// Schema 57 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 57 merges, replace this file with it (and delete
// schemaStandIn57 from the order test's list and its empty-number allowance).

// schemaStandIn57 is the number held for that branch.
const schemaStandIn57 = 57

func init() {
	if len(migrations) != schemaStandIn57-1 {
		panic("the stand-in for migration 57 must follow migrations 1 to 56")
	}
	migrations = append(migrations, migration{additive, ``})
}
