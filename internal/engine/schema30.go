package engine

// Schema 30 (repository mirrors). It is number 30, after the variables
// migration (29), and not 28: the 28 stand-in (schema28.go) has already been
// recorded as applied by any database that ran 29, and a migration is never
// slipped in behind a recorded version.

// schemaMirror is the schema that brought hosted repositories' mirrors.
const schemaMirror = 30

func init() {
	if len(migrations) != schemaMirror-1 {
		panic("migration 30 must follow migrations 1 to 29")
	}
	migrations = append(migrations, mirrorMigration)
}

// 30: mirrors of hosted repositories (see internal/forge/mirror). A pull
//
//	mirror keeps a hosted repository current from an external address; a
//	push mirror copies every accepted update of a hosted repository to an
//	external address. One row per mirror, with its status (last attempt,
//	last success, last error, failures in a row), when it is next due,
//	and a short lease so that two servers do not run one mirror at once.
//	Credentials are references (an environment variable name or a file
//	path), never values. Compatibility: additive: one new table.
var mirrorMigration = migration{additive, `
CREATE TABLE repo_mirrors (
  id           TEXT PRIMARY KEY,
  repo_id      TEXT    NOT NULL,
  direction    TEXT    NOT NULL,
  url          TEXT    NOT NULL,
  username     TEXT    NOT NULL DEFAULT '',
  cred_env     TEXT    NOT NULL DEFAULT '',
  cred_file    TEXT    NOT NULL DEFAULT '',
  interval_secs {{BIG}} NOT NULL DEFAULT 3600,
  enabled      INTEGER NOT NULL DEFAULT 1,
  prune        INTEGER NOT NULL DEFAULT 1,
  state        TEXT    NOT NULL DEFAULT 'pending',
  last_try_at  {{BIG}} NOT NULL DEFAULT 0,
  last_ok_at   {{BIG}} NOT NULL DEFAULT 0,
  last_error   TEXT    NOT NULL DEFAULT '',
  failures     INTEGER NOT NULL DEFAULT 0,
  next_at      {{BIG}} NOT NULL DEFAULT 0,
  synced_gen   {{BIG}} NOT NULL DEFAULT 0,
  dirty_since  {{BIG}} NOT NULL DEFAULT 0,
  ref_count    INTEGER NOT NULL DEFAULT 0,
  lease_holder TEXT    NOT NULL DEFAULT '',
  lease_until  {{BIG}} NOT NULL DEFAULT 0,
  created_at   {{BIG}} NOT NULL DEFAULT 0,
  created_by   TEXT    NOT NULL DEFAULT '',
  UNIQUE (repo_id, direction, url)
);
CREATE UNIQUE INDEX repo_mirrors_one_pull ON repo_mirrors(repo_id) WHERE direction = 'pull';
CREATE INDEX repo_mirrors_due ON repo_mirrors(next_at);
CREATE INDEX repo_mirrors_repo ON repo_mirrors(repo_id);
`}
