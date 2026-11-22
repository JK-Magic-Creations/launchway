package engine

// Schema 62 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 62 merges, replace this file with it.

// schemaStandIn62 is the number held for that branch.
const schemaStandIn62 = 62

func init() {
	if len(migrations) != schemaStandIn62-1 {
		panic("the stand-in for migration 62 must follow migrations 1 to 61")
	}
	migrations = append(migrations, migration{additive, ``})
}
