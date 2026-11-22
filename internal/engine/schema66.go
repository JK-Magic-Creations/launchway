package engine

// Schema 66 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 66 merges, replace this file with it.

// schemaStandIn66 is the number held for that branch.
const schemaStandIn66 = 66

func init() {
	if len(migrations) != schemaStandIn66-1 {
		panic("the stand-in for migration 66 must follow migrations 1 to 65")
	}
	migrations = append(migrations, migration{additive, ``})
}
