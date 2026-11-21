package engine

// Schema 44, what the Debrief (the report of a run's tests) keeps beyond the
// results of each run: one row of statistics per test, which outlives the
// pruning of old results, and the record of which weekly flaky-tests digests
// were sent. It is number 44, after review depth (43).

// schemaDebrief is the schema that brought the Debrief's statistics.
const schemaDebrief = 44

func init() {
	if len(migrations) != schemaDebrief-1 {
		panic("the Debrief migration (44) must follow migrations 1 to 43")
	}
	migrations = append(migrations, debriefMigration)
}

// 44: test_stats has one row per test of a project (by its key): what it is
//
//	called, the run it was first and last seen in, how many runs it ran in
//	and failed in, and its typical duration (a running average) and
//	longest, which survive pruning of the individual results and feed the
//	split of tests among shards. test_flake_digests records, per project
//	and ISO week, that the weekly summary event was sent, so that two
//	servers do not send it twice. coverage_files gains the line numbers a
//	test ran and those it missed, as ranges, for the line view and the
//	coverage of a change. Compatibility: additive: two new tables and two
//	new columns, ignored by an older server. Downgrade: nothing to undo.
var debriefMigration = migration{additive, `
CREATE TABLE test_stats (
  project     TEXT    NOT NULL,
  test_key    TEXT    NOT NULL,
  suite       TEXT    NOT NULL DEFAULT '',
  name        TEXT    NOT NULL DEFAULT '',
  file        TEXT    NOT NULL DEFAULT '',
  variant     TEXT    NOT NULL DEFAULT '',
  first_run   INTEGER NOT NULL DEFAULT 0,
  first_at    {{BIG}} NOT NULL DEFAULT 0,
  last_run    INTEGER NOT NULL DEFAULT 0,
  last_at     {{BIG}} NOT NULL DEFAULT 0,
  runs        INTEGER NOT NULL DEFAULT 0,
  failures    INTEGER NOT NULL DEFAULT 0,
  avg_ms      {{BIG}} NOT NULL DEFAULT 0,
  max_ms      {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, test_key)
);
CREATE INDEX test_stats_last ON test_stats(project, last_at);

ALTER TABLE coverage_files ADD COLUMN covered_lines TEXT NOT NULL DEFAULT '';
ALTER TABLE coverage_files ADD COLUMN missed_lines TEXT NOT NULL DEFAULT '';

CREATE TABLE test_flake_digests (
  project TEXT    NOT NULL,
  week    TEXT    NOT NULL,
  sent_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, week)
);
`}
