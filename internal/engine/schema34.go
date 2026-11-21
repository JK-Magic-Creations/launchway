package engine

// Schema 34 belongs to the encryption-at-rest branch (integrate/encryption)
// and 35 to the build-cache fixes; neither has merged. Until they do, this
// build holds empty stand-ins so that 36 (built-in accounts, schema36.go) is
// the number it will have when all are merged. When the real 34 and 35 merge,
// replace schema34.go and schema35.go with them and set schemaStandIn34 and
// schemaStandIn35 aside in the order test.

// schemaStandIn34 is the number held for the encryption branch.
const schemaStandIn34 = 34

func init() {
	if len(migrations) != schemaStandIn34-1 {
		panic("the stand-in for migration 34 must follow migrations 1 to 33")
	}
	migrations = append(migrations, migration{additive, ``})
}
