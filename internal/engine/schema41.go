package engine

// Schema 41 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 41 merges, replace this file with it.

// schemaStandIn41 is the number held for that branch.
const schemaStandIn41 = 41

func init() {
	if len(migrations) != schemaStandIn41-1 {
		panic("the stand-in for migration 41 must follow migrations 1 to 40")
	}
	migrations = append(migrations, migration{additive, ``})
}
