package engine

// Schema 67 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the numbers after it are the
// numbers they will have when all are merged. The migration-order tests allow
// this stand-in; when the real 67 merges, replace this file with it (and delete
// schemaStandIn67 from the order test's list and its empty-number allowance).

// schemaStandIn67 is the number held for that branch.
const schemaStandIn67 = 67

func init() {
	if len(migrations) != schemaStandIn67-1 {
		panic("the stand-in for migration 67 must follow migrations 1 to 66")
	}
	migrations = append(migrations, migration{additive, ``})
}
