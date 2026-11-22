package engine

// Schema 63 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 63 merges, replace this file with it.

// schemaStandIn63 is the number held for that branch.
const schemaStandIn63 = 63

func init() {
	if len(migrations) != schemaStandIn63-1 {
		panic("the stand-in for migration 63 must follow migrations 1 to 62")
	}
	migrations = append(migrations, migration{additive, ``})
}
