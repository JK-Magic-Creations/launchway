package engine

// Schema 42 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 42 merges, replace this file with it.

// schemaStandIn42 is the number held for that branch.
const schemaStandIn42 = 42

func init() {
	if len(migrations) != schemaStandIn42-1 {
		panic("the stand-in for migration 42 must follow migrations 1 to 41")
	}
	migrations = append(migrations, migration{additive, ``})
}
