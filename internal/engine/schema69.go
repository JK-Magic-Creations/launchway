package engine

// Schema 69 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 69 merges, replace this file with it (and delete
// schemaStandIn69 from the order test's list and its empty-number allowance).

// schemaStandIn69 is the number held for that branch.
const schemaStandIn69 = 69

func init() {
	if len(migrations) != schemaStandIn69-1 {
		panic("the stand-in for migration 69 must follow migrations 1 to 68")
	}
	migrations = append(migrations, migration{additive, ``})
}
