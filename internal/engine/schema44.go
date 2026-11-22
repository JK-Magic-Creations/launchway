package engine

// Schema 44 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 44 merges, replace this file with it.

// schemaStandIn44 is the number held for that branch.
const schemaStandIn44 = 44

func init() {
	if len(migrations) != schemaStandIn44-1 {
		panic("the stand-in for migration 44 must follow migrations 1 to 43")
	}
	migrations = append(migrations, migration{additive, ``})
}
