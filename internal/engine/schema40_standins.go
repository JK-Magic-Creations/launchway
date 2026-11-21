package engine

// Schemas 40 to 55 belong to branches that have not merged. Until they do,
// this build holds empty stand-ins, so that the number after them, 56 (work
// tracking, schema56.go), is the number it will have when all are merged.
// When a real migration merges, replace its stand-in here (delete the number
// from the list) and add its own file; the order of the init functions of
// schema39.go, this file and schema56.go keeps the numbers in a row.

// schemaStandIns40to55 is the last number held for them.
const schemaStandIns40to55 = 55

func init() {
	if len(migrations) != 39 {
		panic("the stand-ins for migrations 40 to 55 must follow migrations 1 to 39")
	}
	for range schemaStandIns40to55 - 39 {
		migrations = append(migrations, migration{additive, ``})
	}
}
