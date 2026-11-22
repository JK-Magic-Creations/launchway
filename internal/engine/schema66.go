package engine

// Schema 66 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 66 merges, replace this file with it (and delete
// schemaStandIn66 from the order test's list and its empty-number allowance).

// schemaStandIn66 is the number held for that branch.
const schemaStandIn66 = 66

func init() {
	if len(migrations) != schemaStandIn66-1 {
		panic("the stand-in for migration 66 must follow migrations 1 to 65")
	}
	migrations = append(migrations, migration{additive, ``})
}
