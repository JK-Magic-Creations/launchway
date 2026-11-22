package engine

// Schema 57 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 57 merges, replace this file with it.

// schemaStandIn57 is the number held for that branch.
const schemaStandIn57 = 57

func init() {
	if len(migrations) != schemaStandIn57-1 {
		panic("the stand-in for migration 57 must follow migrations 1 to 56")
	}
	migrations = append(migrations, migration{additive, ``})
}
