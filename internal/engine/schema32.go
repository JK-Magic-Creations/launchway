package engine

// Schema 32 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the number after it, 33 (pull
// requests, schema33.go), is the number it will have when both are merged.
// The migration-order tests allow this stand-in as they allow the one for 28;
// when the real 32 merges, replace this file with it.

// schemaStandIn32 is the number held for that branch.
const schemaStandIn32 = 32

func init() {
	if len(migrations) != schemaStandIn32-1 {
		panic("the stand-in for migration 32 must follow migrations 1 to 31")
	}
	migrations = append(migrations, migration{additive, ``})
}
