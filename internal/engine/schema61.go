package engine

// Schema 61 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 61 merges, replace this file with it.

// schemaStandIn61 is the number held for that branch.
const schemaStandIn61 = 61

func init() {
	if len(migrations) != schemaStandIn61-1 {
		panic("the stand-in for migration 61 must follow migrations 1 to 60")
	}
	migrations = append(migrations, migration{additive, ``})
}
