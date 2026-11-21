package engine

// Schema 32 belongs to another branch that has not merged. Until it does,
// this build holds an empty stand-in so that the number after the stand-ins,
// 34 (encryption at rest, schema34.go), is the number it will have when all
// are merged. The migration-order tests allow exactly the stand-ins 28, 32
// and 33; when the real ones merge, replace these files with them.

// schemaStandIn32 is the number held for that branch.
const schemaStandIn32 = 32

func init() {
	if len(migrations) != schemaStandIn32-1 {
		panic("the stand-in for migration 32 must follow migrations 1 to 31")
	}
	migrations = append(migrations, migration{additive, ``})
}
