package engine

// Schema 73 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 73 merges, replace this file with it (and delete
// schemaStandIn73 from the order test's list and its empty-number allowance).

// schemaStandIn73 is the number held for that branch.
const schemaStandIn73 = 73

func init() {
	if len(migrations) != schemaStandIn73-1 {
		panic("the stand-in for migration 73 must follow migrations 1 to 72")
	}
	migrations = append(migrations, migration{additive, ``})
}
