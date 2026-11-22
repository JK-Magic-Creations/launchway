package engine

// Schema 64 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 64 merges, replace this file with it.

// schemaStandIn64 is the number held for that branch.
const schemaStandIn64 = 64

func init() {
	if len(migrations) != schemaStandIn64-1 {
		panic("the stand-in for migration 64 must follow migrations 1 to 63")
	}
	migrations = append(migrations, migration{additive, ``})
}
