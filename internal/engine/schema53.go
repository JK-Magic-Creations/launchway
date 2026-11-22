package engine

// Schema 53 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 53 merges, replace this file with it.

// schemaStandIn53 is the number held for that branch.
const schemaStandIn53 = 53

func init() {
	if len(migrations) != schemaStandIn53-1 {
		panic("the stand-in for migration 53 must follow migrations 1 to 52")
	}
	migrations = append(migrations, migration{additive, ``})
}
