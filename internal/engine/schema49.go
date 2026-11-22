package engine

// Schema 49 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 49 merges, replace this file with it.

// schemaStandIn49 is the number held for that branch.
const schemaStandIn49 = 49

func init() {
	if len(migrations) != schemaStandIn49-1 {
		panic("the stand-in for migration 49 must follow migrations 1 to 48")
	}
	migrations = append(migrations, migration{additive, ``})
}
