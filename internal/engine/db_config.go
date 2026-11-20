package engine

// schema23SQL is migration 23: configuration in the database (see configstore.go and
//
//	internal/settings). Every setting an administrator manages
//	(projects, libraries, environments, runner pools, role bindings,
//	policies, health sources, models, secret-store definitions,
//	units and the installation's settings) is an object: one row of
//	config_objects, its current state. config_changes is its history,
//	appended to in the same transaction as every change and never
//	rewritten: who, when, the action, the object before and after;
//	config_objects is what folding it gives. config_imports records
//	each import of a file (the one-time import of the configuration
//	file among them), and config_revisions holds a counter that every
//	change raises, which servers watch to pick changes up. Every table
//	has org, '' for the one organisation there is today, in its key,
//	so organisations can be added without rewriting them.
//	Compatibility: additive: four new tables and nothing else; no new
//	event type, and no run reads them. Older servers ignore the
//	tables and keep using their configuration file, which describes
//	the same installation as long as nothing was changed through the
//	new API (see docs/development/migrations.md).
const schema23SQL = `
CREATE TABLE config_objects (
  org        TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,
  name       TEXT    NOT NULL,
  unit       TEXT    NOT NULL DEFAULT '',
  spec       TEXT    NOT NULL,
  version    {{BIG}} NOT NULL,
  managed_by TEXT    NOT NULL DEFAULT 'api',
  created_at {{BIG}} NOT NULL,
  created_by TEXT    NOT NULL DEFAULT '',
  updated_at {{BIG}} NOT NULL,
  updated_by TEXT    NOT NULL DEFAULT '',
  deleted_at {{BIG}} NOT NULL DEFAULT 0,
  deleted_by TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (org, kind, name)
);

CREATE TABLE config_changes (
  id          {{ID}},
  org         TEXT    NOT NULL DEFAULT '',
  revision    {{BIG}} NOT NULL,
  kind        TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  version     {{BIG}} NOT NULL,
  action      TEXT    NOT NULL,
  actor       TEXT    NOT NULL DEFAULT '',
  at          {{BIG}} NOT NULL,
  managed_by  TEXT    NOT NULL DEFAULT '',
  unit_before TEXT    NOT NULL DEFAULT '',
  unit_after  TEXT    NOT NULL DEFAULT '',
  spec_before TEXT    NOT NULL DEFAULT '',
  spec_after  TEXT    NOT NULL DEFAULT '',
  import_id   TEXT    NOT NULL DEFAULT '',
  comment     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX config_changes_object ON config_changes(org, kind, name, id);
CREATE INDEX config_changes_revision ON config_changes(org, revision);

CREATE TABLE config_imports (
  id       TEXT    PRIMARY KEY,
  org      TEXT    NOT NULL DEFAULT '',
  at       {{BIG}} NOT NULL,
  actor    TEXT    NOT NULL DEFAULT '',
  origin   TEXT    NOT NULL DEFAULT '',
  digest   TEXT    NOT NULL DEFAULT '',
  summary  TEXT    NOT NULL DEFAULT '',
  revision {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE config_revisions (
  org        TEXT    PRIMARY KEY,
  revision   {{BIG}} NOT NULL,
  updated_at {{BIG}} NOT NULL DEFAULT 0
);
`
