package engine

// Schema 54 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 54 merges, replace this file with it.

// schemaStandIn54 is the number held for that branch.
const schemaStandIn54 = 54

func init() {
	if len(migrations) != schemaStandIn54-1 {
		panic("the stand-in for migration 54 must follow migrations 1 to 53")
	}
	migrations = append(migrations, migration{additive, ``})
}
