package engine

// Schemas 57, 58 and 59 belong to other branches that have not merged. Until
// they do, this build holds empty stand-ins so that the numbers after them,
// 60 (notifications people receive, schema60.go) and 61 (verified commits and
// tags, schema61.go), are the numbers they will have when everything is
// merged. The migration-order tests allow exactly these stand-ins; when the
// real 57, 58 and 59 merge, delete this file (and its three constants in the
// order list of migrations_order_test.go).

const (
	schemaStandIn57 = 57
	schemaStandIn58 = 58
	schemaStandIn59 = 59
)

func init() {
	if len(migrations) != schemaStandIn57-1 {
		panic("the stand-ins for migrations 57 to 59 must follow migrations 1 to 56")
	}
	migrations = append(migrations, migration{additive, ``}, migration{additive, ``}, migration{additive, ``})
}
