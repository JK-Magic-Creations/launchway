package engine

// Schema 75: forks and template repositories (docs/guide/forks.md). It is
// number 75, after the numbers other branches hold (57 to 74).

// schemaForks is the schema that brought forks and templates.
const schemaForks = 75

func init() {
	if len(migrations) != schemaForks-1 {
		panic("migration 75 must follow migrations 1 to 74")
	}
	migrations = append(migrations, forksMigration)
}

// 75: forge_repositories gains the fork relationship and the two switches:
//
//	parent_id is the repository this one was forked from and root_id the
//	repository at the top of the chain (both empty for a repository that is
//	no fork); fork_point is the commit of the parent's default branch the
//	fork was made at, forked_by who made it and forked_at when (Unix ms).
//	allow_forks is the repository's switch: -1 follows the default (on for a
//	public repository, off for a private or internal one), 0 off, 1 on.
//	is_template marks a repository people can start new repositories from.
//	owner_fork_settings is the same switch for every repository of one
//	owner: a row per owner that has set it. The repository's own switch
//	wins over the owner's, which wins over the default.
//	Compatibility: additive: columns with defaults and one new table, ignored
//	by an older server (which then shows a fork as an ordinary repository).
//	Downgrade: nothing to undo.
var forksMigration = migration{additive, `
ALTER TABLE forge_repositories ADD COLUMN parent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_repositories ADD COLUMN root_id TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_repositories ADD COLUMN fork_point TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_repositories ADD COLUMN forked_by TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_repositories ADD COLUMN forked_at {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE forge_repositories ADD COLUMN allow_forks INTEGER NOT NULL DEFAULT -1;
ALTER TABLE forge_repositories ADD COLUMN is_template INTEGER NOT NULL DEFAULT 0;
CREATE INDEX forge_repositories_parent ON forge_repositories(parent_id);
CREATE TABLE owner_fork_settings (
  owner       TEXT PRIMARY KEY,
  allow_forks INTEGER NOT NULL,
  updated_by  TEXT    NOT NULL DEFAULT '',
  updated_at  {{BIG}} NOT NULL DEFAULT 0
);
`}
