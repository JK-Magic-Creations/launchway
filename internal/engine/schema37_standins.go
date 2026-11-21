package engine

// Schemas 37 to 44 belong to branches that have not merged. Until they do,
// this build holds empty stand-ins, so that the number after them, 45 (imported
// issues, schema45.go), is the number it will have when all are merged. When a
// real migration merges, replace its stand-in here (delete the number from the
// list) and add its own file; the order of the init functions of schema36.go,
// this file and schema45.go keeps the numbers in a row.

// schemaStandIns37to44 is the last number held for them.
const schemaStandIns37to44 = 44

func init() {
	if len(migrations) != 36 {
		panic("the stand-ins for migrations 37 to 44 must follow migrations 1 to 36")
	}
	for range schemaStandIns37to44 - 36 {
		migrations = append(migrations, migration{additive, ``})
	}
}
