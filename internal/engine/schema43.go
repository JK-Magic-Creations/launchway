package engine

// Schema 43 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 43 merges, replace this file with it.

// schemaStandIn43 is the number held for that branch.
const schemaStandIn43 = 43

func init() {
	if len(migrations) != schemaStandIn43-1 {
		panic("the stand-in for migration 43 must follow migrations 1 to 42")
	}
	migrations = append(migrations, migration{additive, ``})
}
