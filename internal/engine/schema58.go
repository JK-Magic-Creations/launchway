package engine

// Schema 58 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 58 merges, replace this file with it.

// schemaStandIn58 is the number held for that branch.
const schemaStandIn58 = 58

func init() {
	if len(migrations) != schemaStandIn58-1 {
		panic("the stand-in for migration 58 must follow migrations 1 to 57")
	}
	migrations = append(migrations, migration{additive, ``})
}
