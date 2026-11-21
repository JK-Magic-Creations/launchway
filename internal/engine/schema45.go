package engine

// Schema 45, Standing orders (internal/standards): the same scans and best
// practice checks for every repository, on by default. It is number 45,
// after the Debrief (44).

// schemaStandings is the schema that brought Standing orders.
const schemaStandings = 45

func init() {
	if len(migrations) != schemaStandings-1 {
		panic("the Standing orders migration (45) must follow migrations 1 to 44")
	}
	migrations = append(migrations, standingsMigration)
}

// 45: Standing orders.
//
//	findings gains what the one findings model needs beyond the SARIF fields:
//	check_id (the check whose step wrote it), kind, the package, its version
//	and the fixed version, the advisory names, whether the vulnerable code is
//	reachable, a suggested fix, and track: the fingerprint that stays the
//	same when the code around a finding moves.
//	standing_settings holds the settings of a level (installation,
//	organization, unit or project), one JSON document each, with a version.
//	standing_tracked has one row per finding the default branch has had: when
//	it was first seen, last seen, whether it is still open (the backlog).
//	standing_decisions are people's decisions about findings (accepted risk,
//	false positive), with a reason, who and an end date; reopened_at ends one.
//	standing_verdicts is what the gate said about each run. standing_history
//	is one row per project and day: open findings by severity, for the
//	chart.
//	Compatibility: additive: new columns with defaults and new tables. An
//	older server ignores them, and a run that carries Standing orders steps
//	asks for schema 45 (pipeline.SchemaStandings), so no older server runs it.
//	Downgrade: nothing to undo; the data stays and is ignored.
var standingsMigration = migration{additive, `
ALTER TABLE findings ADD COLUMN check_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN kind       TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN package    TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN version    TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN fixed_in   TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN advisories TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN reachable  TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN fix        TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN track      TEXT NOT NULL DEFAULT '';
CREATE INDEX findings_track ON findings(project, track);

CREATE TABLE standing_settings (
  level       TEXT    NOT NULL,
  scope       TEXT    NOT NULL,
  doc         TEXT    NOT NULL DEFAULT '{}',
  version     INTEGER NOT NULL DEFAULT 1,
  updated_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_by  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (level, scope)
);

CREATE TABLE standing_tracked (
  project      TEXT    NOT NULL,
  track        TEXT    NOT NULL,
  check_id     TEXT    NOT NULL DEFAULT '',
  tool         TEXT    NOT NULL DEFAULT '',
  rule_id      TEXT    NOT NULL DEFAULT '',
  severity     TEXT    NOT NULL DEFAULT 'unknown',
  kind         TEXT    NOT NULL DEFAULT '',
  title        TEXT    NOT NULL DEFAULT '',
  file         TEXT    NOT NULL DEFAULT '',
  start_line   INTEGER NOT NULL DEFAULT 0,
  package      TEXT    NOT NULL DEFAULT '',
  version      TEXT    NOT NULL DEFAULT '',
  fixed_in     TEXT    NOT NULL DEFAULT '',
  advisories   TEXT    NOT NULL DEFAULT '',
  reachable    TEXT    NOT NULL DEFAULT '',
  fix          TEXT    NOT NULL DEFAULT '',
  first_run    INTEGER NOT NULL DEFAULT 0,
  first_at     {{BIG}} NOT NULL DEFAULT 0,
  last_run     INTEGER NOT NULL DEFAULT 0,
  last_at      {{BIG}} NOT NULL DEFAULT 0,
  status       TEXT    NOT NULL DEFAULT 'open',
  fixed_run    INTEGER NOT NULL DEFAULT 0,
  fixed_at     {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, track)
);
CREATE INDEX standing_tracked_status ON standing_tracked(project, status, first_at);

CREATE TABLE standing_decisions (
  id          {{ID}},
  project     TEXT    NOT NULL,
  track       TEXT    NOT NULL,
  status      TEXT    NOT NULL,
  reason      TEXT    NOT NULL,
  decided_by  TEXT    NOT NULL DEFAULT '',
  decided_at  {{BIG}} NOT NULL DEFAULT 0,
  expires_at  {{BIG}} NOT NULL DEFAULT 0,
  reopened_at {{BIG}} NOT NULL DEFAULT 0,
  reopened_by TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX standing_decisions_track ON standing_decisions(project, track);

CREATE TABLE standing_verdicts (
  run_id      TEXT    PRIMARY KEY,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  at          {{BIG}} NOT NULL DEFAULT 0,
  blocked     INTEGER NOT NULL DEFAULT 0,
  default_branch INTEGER NOT NULL DEFAULT 0,
  summary     TEXT    NOT NULL DEFAULT '',
  new_count   INTEGER NOT NULL DEFAULT 0,
  existing    INTEGER NOT NULL DEFAULT 0,
  accepted    INTEGER NOT NULL DEFAULT 0,
  detail      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX standing_verdicts_project ON standing_verdicts(project, run_number);

CREATE TABLE standing_history (
  project  TEXT    NOT NULL,
  day      TEXT    NOT NULL,
  total    INTEGER NOT NULL DEFAULT 0,
  critical INTEGER NOT NULL DEFAULT 0,
  high     INTEGER NOT NULL DEFAULT 0,
  medium   INTEGER NOT NULL DEFAULT 0,
  low      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project, day)
);
`}
