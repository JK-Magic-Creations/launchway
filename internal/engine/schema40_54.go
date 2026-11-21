package engine

// Schemas 40 to 54 belong to other branches that have not merged into this
// one (the combined build carries them). Until they do, this build holds
// empty stand-ins so that the number after them, 55 (Standing orders,
// schema55.go), is the number it will have when all are merged. When a real
// migration merges, replace its stand-in (a number in this range) with its
// own file and take the number out of the range the checks in
// migrations_order_test.go allow to be empty.

// The stand-ins are the numbers schemaStandInFirst to schemaStandInLast.
const (
	schemaStandInFirst = 40
	schemaStandInLast  = 54
)

func init() {
	if len(migrations) != schemaStandInFirst-1 {
		panic("the stand-ins for migrations 40 to 54 must follow migrations 1 to 39")
	}
	for n := schemaStandInFirst; n <= schemaStandInLast; n++ {
		migrations = append(migrations, migration{additive, ``})
	}
}
