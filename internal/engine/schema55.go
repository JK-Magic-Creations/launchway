package engine

// Schema 55 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 55 merges, replace this file with it.

// schemaStandIn55 is the number held for that branch.
const schemaStandIn55 = 55

func init() {
	if len(migrations) != schemaStandIn55-1 {
		panic("the stand-in for migration 55 must follow migrations 1 to 54")
	}
	migrations = append(migrations, migration{additive, ``})
}
