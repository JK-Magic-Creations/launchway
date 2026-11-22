package engine

// Schema 65 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 65 merges, replace this file with it.

// schemaStandIn65 is the number held for that branch.
const schemaStandIn65 = 65

func init() {
	if len(migrations) != schemaStandIn65-1 {
		panic("the stand-in for migration 65 must follow migrations 1 to 64")
	}
	migrations = append(migrations, migration{additive, ``})
}
