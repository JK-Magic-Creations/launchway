package engine

// Schemas 40 to 58 belong to other branches that have not merged. Until they
// do, this build holds empty stand-ins so that the number after them, 59
// (layouts and folders, schema59.go), is the number it will have when they are
// all merged. When a real migration merges, replace its number's stand-in
// here (remove it from the range) with the real file, as schema28.go did.

// schemaStandInFirst and schemaStandInLast are the numbers held.
const (
	schemaStandInFirst = 40
	schemaStandInLast  = 58
)

func init() {
	if len(migrations) != schemaStandInFirst-1 {
		panic("the stand-ins for migrations 40 to 58 must follow migrations 1 to 39")
	}
	for n := schemaStandInFirst; n <= schemaStandInLast; n++ {
		migrations = append(migrations, migration{additive, ``})
	}
}
