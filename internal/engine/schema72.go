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
// server checks. work_time_entries holds the time people log on work items: a timer (started_at, and ended_at 0
// while it runs: a unique index allows one running timer per person) or a manual entry; minutes is what counts.
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

CREATE TABLE work_time_entries (
  id         TEXT    NOT NULL PRIMARY KEY,
  item_id    TEXT    NOT NULL,
  who        TEXT    NOT NULL,
  kind       TEXT    NOT NULL DEFAULT 'manual',
  day        TEXT    NOT NULL DEFAULT '',
  started_at {{BIG}} NOT NULL DEFAULT 0,
  ended_at   {{BIG}} NOT NULL DEFAULT 0,
  minutes    INTEGER NOT NULL DEFAULT 0,
  note       TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  updated_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX work_time_item ON work_time_entries(item_id, day);
CREATE INDEX work_time_who ON work_time_entries(who, day);
CREATE UNIQUE INDEX work_time_one_timer ON work_time_entries(who) WHERE kind = 'timer' AND ended_at = 0;
`}
