package engine

// Schema 69 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 69 merges, replace this file with it.

// schemaStandIn69 is the number held for that branch.
const schemaStandIn69 = 69

func init() {
	if len(migrations) != schemaStandIn69-1 {
		panic("the stand-in for migration 69 must follow migrations 1 to 68")
	}
	migrations = append(migrations, migration{additive, ``})
}
