package engine

// schemaStandIn35 is the number held for the build-cache fixes (see schema34.go).
const schemaStandIn35 = 35

func init() {
	if len(migrations) != schemaStandIn35-1 {
		panic("the stand-in for migration 35 must follow migrations 1 to 34")
	}
	migrations = append(migrations, migration{additive, ``})
}
