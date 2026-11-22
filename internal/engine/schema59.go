package engine

// Schema 59 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 59 merges, replace this file with it.

// schemaStandIn59 is the number held for that branch.
const schemaStandIn59 = 59

func init() {
	if len(migrations) != schemaStandIn59-1 {
		panic("the stand-in for migration 59 must follow migrations 1 to 58")
	}
	migrations = append(migrations, migration{additive, ``})
}
