package engine

// Schema 30 and 31 belong to other branches that have not merged. Until they
// do, this build holds empty stand-ins so that the number after them, 32
// (rollback after success, schema32.go), is the number it will have when all
// are merged. The migration-order tests allow exactly these stand-ins (and
// schema 28's); when the real 30 or 31 merges, replace its file with it.

// schemaStandIn30 and schemaStandIn31 are the numbers held for those
// branches.
const (
	schemaStandIn30 = 30
	schemaStandIn31 = 31
)

func init() {
	if len(migrations) != schemaStandIn30-1 {
		panic("the stand-in for migration 30 must follow migrations 1 to 29")
	}
	migrations = append(migrations, migration{additive, ``})
}
