package engine

// Schema 71 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 71 merges, replace this file with it (and delete
// schemaStandIn71 from the order test's list and its empty-number allowance).

// schemaStandIn71 is the number held for that branch.
const schemaStandIn71 = 71

func init() {
	if len(migrations) != schemaStandIn71-1 {
		panic("the stand-in for migration 71 must follow migrations 1 to 70")
	}
	migrations = append(migrations, migration{additive, ``})
}
