package engine

// Schema 48 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 48 merges, replace this file with it.

// schemaStandIn48 is the number held for that branch.
const schemaStandIn48 = 48

func init() {
	if len(migrations) != schemaStandIn48-1 {
		panic("the stand-in for migration 48 must follow migrations 1 to 47")
	}
	migrations = append(migrations, migration{additive, ``})
}
