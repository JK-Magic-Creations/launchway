package engine

// Schema 63 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 63 merges, replace this file with it (and delete
// schemaStandIn63 from the order test's list and its empty-number allowance).

// schemaStandIn63 is the number held for that branch.
const schemaStandIn63 = 63

func init() {
	if len(migrations) != schemaStandIn63-1 {
		panic("the stand-in for migration 63 must follow migrations 1 to 62")
	}
	migrations = append(migrations, migration{additive, ``})
}
