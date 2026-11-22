package engine

// Schema 71 belongs to another branch that has not merged (the real number was given out when the branches were
// planned). Until it does, this build holds an empty stand-in so that schema 72 (reactions, time entries, decision
// records and deployment addresses, with the work of this branch) is the number it will have when all are merged.
// The migration-order tests allow it by number (standInSchema in schema60_68.go). When the real migration merges,
// delete this file and the number 71 from standInSchema, and add the real file.

// schemaStandIn71 is the number held for that branch.
const schemaStandIn71 = 71

func init() {
	if len(migrations) != schemaStandIn71-1 {
		panic("the stand-in for migration 71 must follow migrations 1 to 70")
	}
	migrations = append(migrations, migration{additive, ``})
}
