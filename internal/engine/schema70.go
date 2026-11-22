package engine

// Schema 70 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 70 merges, replace this file with it (and delete
// schemaStandIn70 from the order test's list and its empty-number allowance).

// schemaStandIn70 is the number held for that branch.
const schemaStandIn70 = 70

func init() {
	if len(migrations) != schemaStandIn70-1 {
		panic("the stand-in for migration 70 must follow migrations 1 to 69")
	}
	migrations = append(migrations, migration{additive, ``})
}
