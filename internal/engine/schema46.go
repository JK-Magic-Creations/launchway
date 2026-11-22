package engine

// Schema 46 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 46 merges, replace this file with it.

// schemaStandIn46 is the number held for that branch.
const schemaStandIn46 = 46

func init() {
	if len(migrations) != schemaStandIn46-1 {
		panic("the stand-in for migration 46 must follow migrations 1 to 45")
	}
	migrations = append(migrations, migration{additive, ``})
}
