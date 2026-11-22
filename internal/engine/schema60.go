package engine

// Schema 60 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 60 merges, replace this file with it (and delete
// schemaStandIn60 from the order test's list and its empty-number allowance).

// schemaStandIn60 is the number held for that branch.
const schemaStandIn60 = 60

func init() {
	if len(migrations) != schemaStandIn60-1 {
		panic("the stand-in for migration 60 must follow migrations 1 to 59")
	}
	migrations = append(migrations, migration{additive, ``})
}
