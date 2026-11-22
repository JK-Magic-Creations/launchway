package engine

// Schema 59: Flight badges, old repository addresses and hosting settings
// (see docs/guide/badges.md, docs/guide/repositories.md). It is number 59.

// schemaBadges is the schema that brought them.
const schemaBadges = 59

func init() {
	if len(migrations) != schemaBadges-1 {
		panic("migration 59 must follow migrations 1 to 58")
	}
	migrations = append(migrations, badgesMigration)
}

// 59: flight_badges says which Flights show their status badge to anyone
// (default: off; a Flight of a public repository needs no row). repo_aliases
// are the old addresses (owner/name) of renamed or transferred hosted
// repositories, kept until the address is taken again. hosting_settings are
// the installation's few hosting settings, name and value (archive size limit,
// how long a deleted repository is kept).
//
//	Compatibility: additive: three new tables, ignored by an older server.
//	Downgrade: nothing to undo.
var badgesMigration = migration{additive, `
CREATE TABLE flight_badges (
  project    TEXT PRIMARY KEY,
  enabled    INTEGER NOT NULL DEFAULT 0,
  updated_at {{BIG}} NOT NULL DEFAULT 0,
  updated_by TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE repo_aliases (
  path       TEXT PRIMARY KEY,
  repo_id    TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL DEFAULT 0,
  created_by TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX repo_aliases_repo ON repo_aliases(repo_id);

CREATE TABLE hosting_settings (
  name       TEXT PRIMARY KEY,
  value      TEXT    NOT NULL DEFAULT '',
  updated_at {{BIG}} NOT NULL DEFAULT 0,
  updated_by TEXT    NOT NULL DEFAULT ''
);
`}
