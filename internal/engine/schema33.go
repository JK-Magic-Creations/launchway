package engine

// Schema 33 belongs to another branch that has not merged: an empty
// stand-in, as for 32 (schema32.go). It is named so that its init runs
// after schema32.go's and before schema34.go's.

// schemaStandIn33 is the number held for that branch.
const schemaStandIn33 = 33

func init() {
	if len(migrations) != schemaStandIn33-1 {
		panic("the stand-in for migration 33 must follow migrations 1 to 32")
	}
	migrations = append(migrations, migration{additive, ``})
}
