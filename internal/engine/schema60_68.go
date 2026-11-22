package engine

// Schemas 60 to 68 belong to other branches that have not merged (the real
// numbers were given out when the branches were planned). Until they do, this
// build holds empty stand-ins so that schema 69 (the repository trash, with
// the work of this branch) is the number it will have when all are merged.
// The migration-order tests allow these stand-ins by number. When a real
// migration merges, replace its number here: delete that number from the
// loop's range (keep the others) and add the real file; when all nine are
// real, delete this file and the allowance in migrations_order_test.go and
// upgrade_33_final_test.go (standInSchema). Schema 71 (schema71.go) is held the same way.

const (
	schemaStandInFirst = 60
	schemaStandInLast  = 68
)

// standInSchema reports whether a schema number is held for an unmerged branch.
func standInSchema(n int) bool {
	return n >= schemaStandInFirst && n <= schemaStandInLast || n == schemaStandIn71
}

func init() {
	if len(migrations) != schemaStandInFirst-1 {
		panic("the stand-ins for migrations 60 to 68 must follow migrations 1 to 59")
	}
	for n := schemaStandInFirst; n <= schemaStandInLast; n++ {
		migrations = append(migrations, migration{additive, ``})
	}
}
