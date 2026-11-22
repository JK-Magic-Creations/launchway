package engine

// Schema 59 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 59 merges, replace this file with it (and delete
// schemaStandIn59 from the order test's list and its empty-number allowance).

// schemaStandIn59 is the number held for that branch.
const schemaStandIn59 = 59

func init() {
	if len(migrations) != schemaStandIn59-1 {
		panic("the stand-in for migration 59 must follow migrations 1 to 58")
	}
	migrations = append(migrations, migration{additive, ``})
}
