package engine

// Schema 57: working with a repository that stays on an outside Git host
// (see docs/guides/outside-host.md). It is number 57, after the release
// published to an outside Git host (56).

// schemaHosts is the schema that brought the outside-host connection.
const schemaHosts = 57

func init() {
	if len(migrations) != schemaHosts-1 {
		panic("migration 57 must follow migrations 1 to 56")
	}
	migrations = append(migrations, hostsMigration)
}

// 57: forge_connections gains auth_kind ('token', the only one built; an
//
//	app-installation credential is added later as another value, with
//	app_ref naming its secret, without a new migration), owners (the
//	allow-list of owners or organisations, comma separated, empty means
//	the credential's own reach), webhook_env and webhook_file (a reference
//	to the per-connection webhook secret, never the secret) and
//	upload_url (the documented upload host, for hosts that have one apart
//	from the API). outside_flights ties a project (a Flight) to a
//	repository on a connection and keeps its two report switches (commit
//	statuses, pull-request summary comment); outside_comments remembers the
//	one summary comment per pull request so it is updated in place.
//	Compatibility: additive: one table pair and columns with defaults,
//	ignored by an older server.
//	Downgrade: nothing to undo.
var hostsMigration = migration{additive, `
ALTER TABLE forge_connections ADD COLUMN auth_kind TEXT NOT NULL DEFAULT 'token';
ALTER TABLE forge_connections ADD COLUMN app_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_connections ADD COLUMN owners TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_connections ADD COLUMN webhook_env TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_connections ADD COLUMN webhook_file TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_connections ADD COLUMN upload_url TEXT NOT NULL DEFAULT '';
CREATE TABLE outside_flights (
  project         TEXT PRIMARY KEY,
  connection_id   TEXT    NOT NULL,
  repo_path       TEXT    NOT NULL,
  report_statuses INTEGER NOT NULL DEFAULT 1,
  report_comment  INTEGER NOT NULL DEFAULT 1,
  created_at      {{BIG}} NOT NULL DEFAULT 0,
  updated_at      {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX outside_flights_connection ON outside_flights(connection_id);
CREATE TABLE outside_comments (
  project    TEXT    NOT NULL,
  pr_number  INTEGER NOT NULL,
  comment_id TEXT    NOT NULL,
  updated_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, pr_number)
);
`}
