package engine

// Schema 51 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 51 merges, replace this file with it.

// schemaStandIn51 is the number held for that branch.
const schemaStandIn51 = 51

func init() {
	if len(migrations) != schemaStandIn51-1 {
		panic("the stand-in for migration 51 must follow migrations 1 to 50")
	}
	migrations = append(migrations, migration{additive, ``})
}
