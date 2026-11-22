package engine

// Schema 40 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 40 merges, replace this file with it.

// schemaStandIn40 is the number held for that branch.
const schemaStandIn40 = 40

func init() {
	if len(migrations) != schemaStandIn40-1 {
		panic("the stand-in for migration 40 must follow migrations 1 to 39")
	}
	migrations = append(migrations, migration{additive, ``})
}
