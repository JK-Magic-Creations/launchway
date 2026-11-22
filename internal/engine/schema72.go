package engine

// Schema 72 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 72 merges, replace this file with it (and delete
// schemaStandIn72 from the order test's list and its empty-number allowance).

// schemaStandIn72 is the number held for that branch.
const schemaStandIn72 = 72

func init() {
	if len(migrations) != schemaStandIn72-1 {
		panic("the stand-in for migration 72 must follow migrations 1 to 71")
	}
	migrations = append(migrations, migration{additive, ``})
}
