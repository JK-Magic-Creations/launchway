package engine

// Schemas 40 to 56 belong to branches that have not merged. Until they do, this build holds
// empty stand-ins so that the number after them, 57 (releases, schema57.go), is the number it
// will have when all are merged. The migration-order tests allow exactly these stand-ins and
// no other; when a real migration merges, replace its number here with it.

const (
	schemaStandInFirst = 40
	schemaStandInLast  = 56
)

func init() {
	if len(migrations) != schemaStandInFirst-1 {
		panic("the stand-ins for migrations 40 to 56 must follow migrations 1 to 39")
	}
	for n := schemaStandInFirst; n <= schemaStandInLast; n++ {
		migrations = append(migrations, migration{additive, ``})
	}
}
