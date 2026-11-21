package engine

// The stand-in for schema 31: see schema30.go.

func init() {
	if len(migrations) != schemaStandIn31-1 {
		panic("the stand-in for migration 31 must follow migrations 1 to 30")
	}
	migrations = append(migrations, migration{additive, ``})
}
