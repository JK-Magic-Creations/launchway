package engine

// Schema 52 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 52 merges, replace this file with it.

// schemaStandIn52 is the number held for that branch.
const schemaStandIn52 = 52

func init() {
	if len(migrations) != schemaStandIn52-1 {
		panic("the stand-in for migration 52 must follow migrations 1 to 51")
	}
	migrations = append(migrations, migration{additive, ``})
}
