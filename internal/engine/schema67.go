package engine

// Schema 67 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that migration 70 (the wall between
// Bases, schema70.go) is number 70 whichever of the queued branches merge
// first. When the real 67 merges, replace this file with it.

// schemaStandIn67 is the number held for that branch.
const schemaStandIn67 = 67

func init() {
	if len(migrations) != schemaStandIn67-1 {
		panic("the stand-in for migration 67 must follow migrations 1 to 66")
	}
	migrations = append(migrations, migration{additive, ``})
}
