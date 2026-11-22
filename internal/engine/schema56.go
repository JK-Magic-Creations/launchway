package engine

// Schema 56 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 56 merges, replace this file with it.

// schemaStandIn56 is the number held for that branch.
const schemaStandIn56 = 56

func init() {
	if len(migrations) != schemaStandIn56-1 {
		panic("the stand-in for migration 56 must follow migrations 1 to 55")
	}
	migrations = append(migrations, migration{additive, ``})
}
