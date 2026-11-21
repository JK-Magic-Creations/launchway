package engine

// Schema 48, configuration as code: the run reports of Ansible and
// OpenTofu/Terraform steps, and the built-in state backend (workspaces,
// sealed state versions, locks, stored plans). It is number 48, after the graph (47).

// schemaConfigAsCode is the schema that brought configuration as code.
const schemaConfigAsCode = 48

func init() {
	if len(migrations) != schemaConfigAsCode-1 {
		panic("the configuration-as-code migration (48) must follow migrations 1 to 47")
	}
	migrations = append(migrations, configAsCodeMigration)
}

// 48: cac_reports keeps one parsed report per step attempt (kind ansible or
//
//	plan), already redacted, with its recap. tf_workspaces is one state
//	address (project, workspace). tf_state_versions are its sealed versions
//	(sealed with the keyring, bound to the row): who, when, which run, which
//	serial. tf_locks is the lock of a workspace. Plan files travel as the
//	run's artifacts (bound to the run's commit). Compatibility: additive: new tables, ignored by an older server.
//	Downgrade: nothing to undo.
var configAsCodeMigration = migration{additive, `
CREATE TABLE cac_reports (
  id          TEXT    NOT NULL PRIMARY KEY,
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  environment TEXT    NOT NULL DEFAULT '',
  kind        TEXT    NOT NULL,
  commit_sha  TEXT    NOT NULL DEFAULT '',
  check_mode  INTEGER NOT NULL DEFAULT 0,
  status      TEXT    NOT NULL DEFAULT '',
  recap       TEXT    NOT NULL DEFAULT '',
  summary     TEXT    NOT NULL DEFAULT '{}',
  document    TEXT    NOT NULL DEFAULT '{}',
  created_at  {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX cac_reports_attempt ON cac_reports(run_id, step_id, attempt, kind);
CREATE INDEX cac_reports_project ON cac_reports(project, environment, kind, created_at);

CREATE TABLE tf_workspaces (
  project         TEXT    NOT NULL,
  name            TEXT    NOT NULL,
  lineage         TEXT    NOT NULL DEFAULT '',
  serial          {{BIG}} NOT NULL DEFAULT 0,
  version         INTEGER NOT NULL DEFAULT 0,
  environment     TEXT    NOT NULL DEFAULT '',
  created_at      {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, name)
);

CREATE TABLE tf_state_versions (
  id               TEXT    NOT NULL PRIMARY KEY,
  project          TEXT    NOT NULL,
  workspace        TEXT    NOT NULL,
  version          INTEGER NOT NULL,
  serial           {{BIG}} NOT NULL DEFAULT 0,
  lineage          TEXT    NOT NULL DEFAULT '',
  sha256           TEXT    NOT NULL DEFAULT '',
  size             {{BIG}} NOT NULL DEFAULT 0,
  sealed           TEXT    NOT NULL,
  resources        INTEGER NOT NULL DEFAULT 0,
  source           TEXT    NOT NULL DEFAULT 'write',
  restored_from    INTEGER NOT NULL DEFAULT 0,
  run_id           TEXT    NOT NULL DEFAULT '',
  actor            TEXT    NOT NULL DEFAULT '',
  created_at       {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX tf_state_versions_n ON tf_state_versions(project, workspace, version);

CREATE TABLE tf_locks (
  project    TEXT    NOT NULL,
  workspace  TEXT    NOT NULL,
  lock_id    TEXT    NOT NULL,
  info       TEXT    NOT NULL DEFAULT '{}',
  run_id     TEXT    NOT NULL DEFAULT '',
  actor      TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, workspace)
);
`}
