package engine

// Schema 68 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 68 merges, replace this file with it.

// schemaStandIn68 is the number held for that branch.
const schemaStandIn68 = 68

func init() {
	if len(migrations) != schemaStandIn68-1 {
		panic("the stand-in for migration 68 must follow migrations 1 to 67")
	}
	migrations = append(migrations, migration{additive, ``})
}
