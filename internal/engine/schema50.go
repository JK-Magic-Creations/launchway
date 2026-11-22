package engine

// Schema 50 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 50 merges, replace this file with it.

// schemaStandIn50 is the number held for that branch.
const schemaStandIn50 = 50

func init() {
	if len(migrations) != schemaStandIn50-1 {
		panic("the stand-in for migration 50 must follow migrations 1 to 49")
	}
	migrations = append(migrations, migration{additive, ``})
}
