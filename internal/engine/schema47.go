package engine

// Schema 47 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 47 merges, replace this file with it.

// schemaStandIn47 is the number held for that branch.
const schemaStandIn47 = 47

func init() {
	if len(migrations) != schemaStandIn47-1 {
		panic("the stand-in for migration 47 must follow migrations 1 to 46")
	}
	migrations = append(migrations, migration{additive, ``})
}
