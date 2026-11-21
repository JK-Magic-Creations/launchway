package engine

// Schema 42 (the event log). It is number 42, after feature toggles (41)
// (schema41.go).

// schemaEvents is the schema that brought the event log, product-managed
// event sources, and triggers on another run's result.
const schemaEvents = 42

func init() {
	if len(migrations) != schemaEvents-1 {
		panic("the event log migration (42) must follow migrations 1 to 41")
	}
	migrations = append(migrations, eventsMigration)
}

// 42: the event log: every event the server received, as a record, and the
//
//	decision made for each trigger it was judged against, with the reason.
//	event_sources are incoming webhook sources made in the product (a sealed
//	signing secret, an optional schema, a mapping to typed inputs, a filter, a
//	dedupe key, a debounce or batch window); event_dedupe remembers keys;
//	event_windows holds a window that is open; event_cursors remembers how far
//	a server has read run results; run_triggers start a run when another
//	project's run (or Launch) ends, and run_trigger_allow is the upstream
//	project's permission for that. Compatibility: additive: new tables only.
var eventsMigration = migration{additive, `
CREATE TABLE event_log (
  id          TEXT PRIMARY KEY,
  project     TEXT    NOT NULL,
  at          {{BIG}} NOT NULL,
  source      TEXT    NOT NULL,
  source_name TEXT    NOT NULL DEFAULT '',
  verified    INTEGER NOT NULL DEFAULT 1,
  summary     TEXT    NOT NULL DEFAULT '',
  ref         TEXT    NOT NULL DEFAULT '',
  sha         TEXT    NOT NULL DEFAULT '',
  dedupe_key  TEXT    NOT NULL DEFAULT '',
  payload     TEXT    NOT NULL DEFAULT '',
  replay      TEXT    NOT NULL DEFAULT '',
  replay_of   TEXT    NOT NULL DEFAULT '',
  actor       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX event_log_project ON event_log(project, at DESC);
CREATE INDEX event_log_at ON event_log(at);

CREATE TABLE event_decisions (
  id           {{ID}},
  event_id     TEXT    NOT NULL,
  project      TEXT    NOT NULL,
  at           {{BIG}} NOT NULL,
  trigger_name TEXT    NOT NULL DEFAULT '',
  decision     TEXT    NOT NULL,
  reason       TEXT    NOT NULL DEFAULT '',
  run_id       TEXT    NOT NULL DEFAULT '',
  run_number   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX event_decisions_event ON event_decisions(event_id);
CREATE INDEX event_decisions_run ON event_decisions(project, run_number);

CREATE TABLE event_sources (
  id               TEXT PRIMARY KEY,
  project          TEXT    NOT NULL,
  name             TEXT    NOT NULL,
  secret_sealed    TEXT    NOT NULL DEFAULT '',
  schema_json      TEXT    NOT NULL DEFAULT '',
  mapping          TEXT    NOT NULL DEFAULT '{}',
  filter_expr      TEXT    NOT NULL DEFAULT '',
  dedupe_expr      TEXT    NOT NULL DEFAULT '',
  ref              TEXT    NOT NULL DEFAULT '',
  debounce_secs    INTEGER NOT NULL DEFAULT 0,
  batch_secs       INTEGER NOT NULL DEFAULT 0,
  max_body         INTEGER NOT NULL DEFAULT 65536,
  rate_per_min     INTEGER NOT NULL DEFAULT 60,
  enabled          INTEGER NOT NULL DEFAULT 1,
  created_at       {{BIG}} NOT NULL DEFAULT 0,
  created_by       TEXT    NOT NULL DEFAULT '',
  last_received_at {{BIG}} NOT NULL DEFAULT 0,
  UNIQUE (project, name)
);

CREATE TABLE event_dedupe (
  project  TEXT    NOT NULL,
  scope    TEXT    NOT NULL,
  dkey     TEXT    NOT NULL,
  at       {{BIG}} NOT NULL,
  event_id TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (project, scope, dkey)
);

CREATE TABLE event_windows (
  project   TEXT    NOT NULL,
  source    TEXT    NOT NULL,
  mode      TEXT    NOT NULL,
  opened_at {{BIG}} NOT NULL,
  due_at    {{BIG}} NOT NULL,
  count     INTEGER NOT NULL DEFAULT 1,
  event_id  TEXT    NOT NULL DEFAULT '',
  inputs    TEXT    NOT NULL DEFAULT '{}',
  PRIMARY KEY (project, source)
);

CREATE TABLE event_cursors (
  name TEXT PRIMARY KEY,
  seq  {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE run_triggers (
  id               TEXT PRIMARY KEY,
  project          TEXT    NOT NULL,
  name             TEXT    NOT NULL,
  kind             TEXT    NOT NULL DEFAULT 'run',
  upstream_project TEXT    NOT NULL,
  upstream_pipeline TEXT   NOT NULL DEFAULT '',
  when_result      TEXT    NOT NULL DEFAULT 'succeeded',
  filter_expr      TEXT    NOT NULL DEFAULT '',
  ref              TEXT    NOT NULL DEFAULT '',
  mapping          TEXT    NOT NULL DEFAULT '{}',
  enabled          INTEGER NOT NULL DEFAULT 1,
  created_at       {{BIG}} NOT NULL DEFAULT 0,
  created_by       TEXT    NOT NULL DEFAULT '',
  UNIQUE (project, name)
);

CREATE TABLE run_trigger_allow (
  upstream_project   TEXT    NOT NULL,
  downstream_project TEXT    NOT NULL,
  share_outputs      INTEGER NOT NULL DEFAULT 0,
  created_at         {{BIG}} NOT NULL DEFAULT 0,
  created_by         TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (upstream_project, downstream_project)
);
`}
