package engine

// Schema 26 (forges). It lives in its own file so that db.go, which is over
// the size limit, does not grow. The file is named so that its init runs
// after schema25.go's: the migrations are appended in file-name order.

// schemaForge is the schema that brought forge connections and repositories.
const schemaForge = 26

func init() {
	if len(migrations) != schemaForge-1 {
		panic("the forges migration (26) must follow migrations 1 to 25")
	}
	migrations = append(migrations, forgeMigration)
}

// 26: forges (see internal/forge). Connections and repositories: a
//
//	connection is a configured link to a Git host (kind plain is
//	today's watcher of a Git URL) and a repository is one repository
//	on a connection; every watched project becomes a repository on the
//	plain connection. forge_events keeps what each connection heard,
//	with a retention limit, so that "why did this push not start a
//	run?" has an answer; forge_deliveries remembers webhook deliveries
//	per connection so a replay is ignored. Credentials are references
//	(an environment variable name or a file path), never values.
//	Compatibility: additive: new tables and nothing else. Nothing
//	older code reads changes, and nothing is turned on by the tables.
//	The repo_* tables are the storage of hosted repositories (internal/
//	forge/storage): the refs and a manifest of immutable packs, which
//	live in the blob store, and a generation number per repository that
//	every server's disposable cache is checked against.
var forgeMigration = migration{additive, `
CREATE TABLE forge_connections (
  id         TEXT PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,
  kind       TEXT    NOT NULL,
  base_url   TEXT    NOT NULL DEFAULT '',
  cred_env   TEXT    NOT NULL DEFAULT '',
  cred_file  TEXT    NOT NULL DEFAULT '',
  events     TEXT    NOT NULL DEFAULT '',
  enabled    INTEGER NOT NULL DEFAULT 1,
  created_at {{BIG}} NOT NULL DEFAULT 0,
  updated_at {{BIG}} NOT NULL DEFAULT 0,
  created_by TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE forge_repositories (
  id             TEXT PRIMARY KEY,
  connection_id  TEXT    NOT NULL,
  home           TEXT    NOT NULL DEFAULT '',
  name           TEXT    NOT NULL,
  path           TEXT    NOT NULL,
  project        TEXT    NOT NULL DEFAULT '',
  default_branch TEXT    NOT NULL DEFAULT '',
  visibility     TEXT    NOT NULL DEFAULT 'private',
  archived       INTEGER NOT NULL DEFAULT 0,
  mirror         INTEGER NOT NULL DEFAULT 0,
  clone_https    TEXT    NOT NULL DEFAULT '',
  clone_ssh      TEXT    NOT NULL DEFAULT '',
  created_at     {{BIG}} NOT NULL DEFAULT 0,
  updated_at     {{BIG}} NOT NULL DEFAULT 0,
  UNIQUE (connection_id, path)
);
CREATE INDEX forge_repositories_project ON forge_repositories(project);

CREATE TABLE forge_events (
  seq           {{ID}},
  connection_id TEXT    NOT NULL,
  repository_id TEXT    NOT NULL DEFAULT '',
  kind          TEXT    NOT NULL,
  ref           TEXT    NOT NULL DEFAULT '',
  old_sha       TEXT    NOT NULL DEFAULT '',
  new_sha       TEXT    NOT NULL DEFAULT '',
  outcome       TEXT    NOT NULL DEFAULT '',
  detail        TEXT    NOT NULL DEFAULT '',
  at            {{BIG}} NOT NULL
);
CREATE INDEX forge_events_connection ON forge_events(connection_id, seq);

CREATE TABLE forge_deliveries (
  connection_id TEXT    NOT NULL,
  delivery_id   TEXT    NOT NULL,
  at            {{BIG}} NOT NULL,
  PRIMARY KEY (connection_id, delivery_id)
);

CREATE TABLE repo_generation (
  repo_id    TEXT PRIMARY KEY,
  gen        {{BIG}} NOT NULL DEFAULT 0,
  flagged    TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE repo_refs (
  repo_id     TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  target      TEXT    NOT NULL,
  updated_gen {{BIG}} NOT NULL,
  PRIMARY KEY (repo_id, name)
);

CREATE TABLE repo_packs (
  repo_id    TEXT    NOT NULL,
  pack_id    TEXT    NOT NULL,
  blob_key   TEXT    NOT NULL,
  bytes      {{BIG}} NOT NULL,
  objects    {{BIG}} NOT NULL DEFAULT 0,
  state      TEXT    NOT NULL,
  born_gen   {{BIG}} NOT NULL DEFAULT 0,
  died_gen   {{BIG}} NOT NULL DEFAULT 0,
  created_at {{BIG}} NOT NULL DEFAULT 0,
  retired_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_id, pack_id)
);
CREATE INDEX repo_packs_state ON repo_packs(repo_id, state);

CREATE TABLE repo_maintenance (
  repo_id  TEXT PRIMARY KEY,
  holder   TEXT    NOT NULL,
  until_at {{BIG}} NOT NULL
);
`}
