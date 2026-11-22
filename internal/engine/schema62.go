package engine

// Schema 62: what a step attempt's sizing was and whether it was killed for memory get columns of their own.

// schemaSizing is the schema that gave step_usage its sizing columns.
const schemaSizing = 62

func init() {
	if len(migrations) != schemaSizing-1 {
		panic("migration 62 must follow migrations 1 to 61")
	}
	migrations = append(migrations, sizingMigration)
}

// us is the unit separator that a development build of the sizing work wrote at the front of step_usage.note, around
// "sizing=learned;runs=7;killed=1", before there was a column for it. Migration 62 moves such notes into the columns.
const us = "\x1f"

// sizingMeta is the text between the two separators; the rows it is used on start with a separator.
const sizingMeta = `SUBSTR(note, 2, {{INSTR}}(SUBSTR(note, 2), '` + us + `') - 1)`

// 62: step_usage gets sizing (where the step's memory and CPU came from: declared, learned, default or raised, or empty
//
//	when nothing was recorded), sizing_runs (how many measured runs a learned figure rests on) and killed (1 when the
//	attempt was killed for memory). A note that carries the old prefix is split: its three values go to the columns
//	and the prefix is cut off.
//	Compatibility: additive: three columns with defaults; an older server neither reads nor writes them (its notes
//	are not prefixed, so nothing is lost).
//	Downgrade: nothing to undo.
var sizingMigration = migration{additive, `
ALTER TABLE step_usage ADD COLUMN sizing TEXT NOT NULL DEFAULT '';
ALTER TABLE step_usage ADD COLUMN sizing_runs {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE step_usage ADD COLUMN killed INTEGER NOT NULL DEFAULT 0;
UPDATE step_usage SET
  sizing = CASE
    WHEN ` + sizingMeta + ` LIKE 'sizing=declared%' THEN 'declared'
    WHEN ` + sizingMeta + ` LIKE 'sizing=learned%' THEN 'learned'
    WHEN ` + sizingMeta + ` LIKE 'sizing=default%' THEN 'default'
    WHEN ` + sizingMeta + ` LIKE 'sizing=raised%' THEN 'raised'
    ELSE '' END,
  killed = CASE WHEN ` + sizingMeta + ` LIKE '%;killed=1' THEN 1 ELSE 0 END,
  sizing_runs = CASE WHEN {{INSTR}}(` + sizingMeta + `, ';runs=') > 0 THEN CAST(
    SUBSTR(SUBSTR(` + sizingMeta + `, {{INSTR}}(` + sizingMeta + `, ';runs=') + 6) || ';', 1,
      {{INSTR}}(SUBSTR(` + sizingMeta + `, {{INSTR}}(` + sizingMeta + `, ';runs=') + 6) || ';', ';') - 1) AS {{BIG}}) ELSE 0 END
  WHERE SUBSTR(note, 1, 1) = '` + us + `' AND {{INSTR}}(SUBSTR(note, 2), '` + us + `') > 0;
UPDATE step_usage SET note = SUBSTR(note, {{INSTR}}(SUBSTR(note, 2), '` + us + `') + 2)
  WHERE SUBSTR(note, 1, 1) = '` + us + `' AND {{INSTR}}(SUBSTR(note, 2), '` + us + `') > 0;
`}
