package engine

// Schema 41, feature toggles (internal/toggles, internal/togglestore), in a
// file of its own: db.go is over the size ratchet. It follows the built-in
// registry (40).

// schemaToggles is the schema that brought feature toggles.
const schemaToggles = 41

func init() {
	if len(migrations) != schemaToggles-1 {
		panic("the toggles migration (41) must follow migrations 1 to 40")
	}
	migrations = append(migrations, togglesMigration)
}

// 41: feature toggles.
//
//	toggles: a toggle's definition, at a level (installation, organization,
//	unit or project; scope is the unit's or the project's name, empty for
//	the two above), as variables are: a nearer level overrides, a lock set at
//	a level binds below. toggle_envs: its state in one environment (on or
//	off, the rules, the value served when no rule matches), as JSON, with
//	the moment it began to serve one value to everyone (for the stale
//	nudge). toggle_segments: reusable named rule sets. toggle_changes: every
//	change, who, when, why, before and after, and whether it waits for an
//	approval; the history and the revert are built on it. toggle_keys: the
//	SDK keys of an environment, stored as hashes (server keys) or as the
//	public identifier (client keys), never the secret. toggle_schedules:
//	changes to apply later. toggle_safeguards: the safeguards at a level.
//	toggle_evals: counts of evaluations per toggle, environment, day and
//	value, with no information about who was evaluated. toggle_revs: one
//	counter per organization, raised in the transaction of every change, that
//	the servers read to learn that their in-memory snapshots are out of date.
//	Compatibility: additive: new tables and nothing else; nothing older code
//	reads changes. Downgrade: nothing to undo for an older server; the tables
//	stay and are ignored.
var togglesMigration = migration{additive, `
CREATE TABLE toggles (
  org            TEXT    NOT NULL DEFAULT 'default',
  level          TEXT    NOT NULL,
  scope          TEXT    NOT NULL DEFAULT '',
  key            TEXT    NOT NULL,
  type           TEXT    NOT NULL,
  kind           TEXT    NOT NULL DEFAULT 'release',
  description    TEXT    NOT NULL DEFAULT '',
  salt           TEXT    NOT NULL DEFAULT '',
  default_value  TEXT    NOT NULL DEFAULT 'null',
  safe_value     TEXT    NOT NULL DEFAULT 'null',
  client_visible INTEGER NOT NULL DEFAULT 0,
  owner          TEXT    NOT NULL DEFAULT '',
  tags           TEXT    NOT NULL DEFAULT '',
  expires_at     {{BIG}} NOT NULL DEFAULT 0,
  origin         TEXT    NOT NULL DEFAULT '',
  origin_change  {{BIG}} NOT NULL DEFAULT 0,
  locked         INTEGER NOT NULL DEFAULT 0,
  lock_reason    TEXT    NOT NULL DEFAULT '',
  locked_by      TEXT    NOT NULL DEFAULT '',
  version        INTEGER NOT NULL DEFAULT 1,
  created_at     {{BIG}} NOT NULL DEFAULT 0,
  created_by     TEXT    NOT NULL DEFAULT '',
  updated_at     {{BIG}} NOT NULL DEFAULT 0,
  updated_by     TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (org, level, scope, key)
);

CREATE TABLE toggle_envs (
  org           TEXT    NOT NULL DEFAULT 'default',
  level         TEXT    NOT NULL,
  scope         TEXT    NOT NULL DEFAULT '',
  key           TEXT    NOT NULL,
  environment   TEXT    NOT NULL,
  state         TEXT    NOT NULL DEFAULT '{}',
  settled       TEXT    NOT NULL DEFAULT '',
  settled_since {{BIG}} NOT NULL DEFAULT 0,
  version       INTEGER NOT NULL DEFAULT 1,
  updated_at    {{BIG}} NOT NULL DEFAULT 0,
  updated_by    TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (org, level, scope, key, environment)
);

CREATE TABLE toggle_segments (
  org         TEXT    NOT NULL DEFAULT 'default',
  level       TEXT    NOT NULL,
  scope       TEXT    NOT NULL DEFAULT '',
  name        TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  definition  TEXT    NOT NULL DEFAULT '{}',
  version     INTEGER NOT NULL DEFAULT 1,
  updated_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_by  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (org, level, scope, name)
);

CREATE TABLE toggle_changes (
  id           {{ID}},
  org          TEXT    NOT NULL DEFAULT 'default',
  at           {{BIG}} NOT NULL,
  project      TEXT    NOT NULL DEFAULT '',
  level        TEXT    NOT NULL,
  scope        TEXT    NOT NULL DEFAULT '',
  key          TEXT    NOT NULL,
  environment  TEXT    NOT NULL DEFAULT '',
  action       TEXT    NOT NULL,
  actor        TEXT    NOT NULL DEFAULT '',
  reason       TEXT    NOT NULL DEFAULT '',
  via          TEXT    NOT NULL DEFAULT '',
  status       TEXT    NOT NULL DEFAULT 'applied',
  old_value    TEXT    NOT NULL DEFAULT '',
  new_value    TEXT    NOT NULL DEFAULT '',
  approvals    TEXT    NOT NULL DEFAULT '[]',
  decided_by   TEXT    NOT NULL DEFAULT '',
  decided_at   {{BIG}} NOT NULL DEFAULT 0,
  reverts      {{BIG}} NOT NULL DEFAULT 0,
  reverted_by  {{BIG}} NOT NULL DEFAULT 0,
  watch_until  {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX toggle_changes_by_key ON toggle_changes (org, key, id);
CREATE INDEX toggle_changes_by_env ON toggle_changes (org, project, environment, id);

CREATE TABLE toggle_keys (
  id          TEXT PRIMARY KEY,
  org         TEXT    NOT NULL DEFAULT 'default',
  project     TEXT    NOT NULL,
  environment TEXT    NOT NULL,
  kind        TEXT    NOT NULL,
  name        TEXT    NOT NULL DEFAULT '',
  prefix      TEXT    NOT NULL DEFAULT '',
  secret_hash TEXT    NOT NULL DEFAULT '',
  origins     TEXT    NOT NULL DEFAULT '',
  created_at  {{BIG}} NOT NULL,
  created_by  TEXT    NOT NULL DEFAULT '',
  expires_at  {{BIG}} NOT NULL DEFAULT 0,
  revoked_at  {{BIG}} NOT NULL DEFAULT 0,
  rotated_to  TEXT    NOT NULL DEFAULT '',
  last_used_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX toggle_keys_by_env ON toggle_keys (org, project, environment);

CREATE TABLE toggle_schedules (
  id          {{ID}},
  org         TEXT    NOT NULL DEFAULT 'default',
  project     TEXT    NOT NULL,
  level       TEXT    NOT NULL,
  scope       TEXT    NOT NULL DEFAULT '',
  key         TEXT    NOT NULL,
  environment TEXT    NOT NULL,
  run_at      {{BIG}} NOT NULL,
  patch       TEXT    NOT NULL,
  reason      TEXT    NOT NULL DEFAULT '',
  created_by  TEXT    NOT NULL DEFAULT '',
  created_at  {{BIG}} NOT NULL,
  status      TEXT    NOT NULL DEFAULT 'pending',
  result      TEXT    NOT NULL DEFAULT '',
  done_at     {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX toggle_schedules_due ON toggle_schedules (status, run_at);

CREATE TABLE toggle_safeguards (
  org         TEXT    NOT NULL DEFAULT 'default',
  level       TEXT    NOT NULL,
  scope       TEXT    NOT NULL DEFAULT '',
  environment TEXT    NOT NULL DEFAULT '',
  settings    TEXT    NOT NULL DEFAULT '{}',
  locked      TEXT    NOT NULL DEFAULT '',
  version     INTEGER NOT NULL DEFAULT 1,
  updated_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_by  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (org, level, scope, environment)
);

CREATE TABLE toggle_evals (
  org         TEXT    NOT NULL DEFAULT 'default',
  project     TEXT    NOT NULL,
  key         TEXT    NOT NULL,
  environment TEXT    NOT NULL,
  day         {{BIG}} NOT NULL,
  variation   TEXT    NOT NULL,
  reason      TEXT    NOT NULL,
  count       {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (org, project, key, environment, day, variation, reason)
);

CREATE TABLE toggle_revs (
  org TEXT PRIMARY KEY,
  rev {{BIG}} NOT NULL DEFAULT 0
);
`}
