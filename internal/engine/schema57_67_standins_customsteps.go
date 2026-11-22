package engine

// Schemas 57 to 67 belong to other branches that have not merged. Until they
// do, this build holds empty stand-ins so that custom steps' migration, 68
// (schema68.go), has the number it will have when all are merged. The
// migration-order tests allow exactly these stand-ins (and 28). When the
// other branches merge, their real migrations replace these numbers' entries
// and this file is deleted (the Depot branch holds the same numbers 57 to 65
// in schema57_65_standins.go; keep one set).
//
// A stand-in is additive and empty: a database at 67 differs from one at 56
// only in its recorded schema number.

const (
	customStandInFirst = 57
	customStandInLast  = 67
)

func init() {
	if len(migrations) != customStandInFirst-1 {
		panic("the stand-ins for migrations 57 to 67 must follow migrations 1 to 56")
	}
	for n := customStandInFirst; n <= customStandInLast; n++ {
		migrations = append(migrations, migration{additive, ``})
	}
}

// isCustomStandIn says whether schema n is an empty stand-in held for another branch.
func isCustomStandIn(n int) bool {
	return n == schemaStandIn28 || (n >= customStandInFirst && n <= customStandInLast)
}
