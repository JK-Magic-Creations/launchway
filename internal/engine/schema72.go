package engine

// Schema 72: reactions on work items and comments, time entries on work items, an address on a deployment and the
// decision record's links (see docs/guide/work.md). It is number 72, after the stand-in for 71.

// schemaWorkExtras is the schema that brought them.
const schemaWorkExtras = 72

func init() {
	if len(migrations) != schemaWorkExtras-1 {
		panic("migration 72 must follow migrations 1 to 71")
	}
	migrations = append(migrations, workExtrasMigration)
}

// 72: work_reactions has one row per person, target (an item or a comment) and reaction, from a fixed set that the
// server checks.
//
//	Compatibility: additive: new tables (and columns with defaults), ignored by an older server.
//	Downgrade: nothing to undo.
var workExtrasMigration = migration{additive, `
CREATE TABLE work_reactions (
  target_kind TEXT    NOT NULL,
  target_id   TEXT    NOT NULL,
  who         TEXT    NOT NULL,
  reaction    TEXT    NOT NULL,
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (target_kind, target_id, who, reaction)
);
`}
