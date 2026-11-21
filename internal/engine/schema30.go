package engine

// Schema 30 belongs to another branch (repository mirrors) that has not
// merged. Until it does, this build holds an empty stand-in so that the
// number after it, 31 (SSH keys and webhooks, schema31.go), is the number it
// will have when both are merged. When the real 30 merges, replace this file
// with it and the stand-in checks in migrations_order_test.go with its own.

// schemaStandIn30 is the number held for that branch.
const schemaStandIn30 = 30

func init() {
	if len(migrations) != schemaStandIn30-1 {
		panic("the stand-in for migration 30 must follow migrations 1 to 29")
	}
	migrations = append(migrations, migration{additive, ``})
}
