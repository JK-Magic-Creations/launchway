package engine

// Schema 45 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 45 merges, replace this file with it.

// schemaStandIn45 is the number held for that branch.
const schemaStandIn45 = 45

func init() {
	if len(migrations) != schemaStandIn45-1 {
		panic("the stand-in for migration 45 must follow migrations 1 to 44")
	}
	migrations = append(migrations, migration{additive, ``})
}
