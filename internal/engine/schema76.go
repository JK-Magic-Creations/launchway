package engine

// Schema 76: pull requests from forks (docs/guide/forks.md).

// schemaForkPulls is the schema that brought them.
const schemaForkPulls = 76

func init() {
	if len(migrations) != schemaForkPulls-1 {
		panic("migration 76 must follow migrations 1 to 75")
	}
	migrations = append(migrations, forkPullsMigration)
}

// 76: pull_requests gains allow_maintainer_edits (1: the people who may write
// to the target repository may push to the source branch in the fork; 0, the
// default, they may not) and run_approved_head (the head commit a person with
// write access to the target repository approved to run its pipeline, for a
// fork's first-time contributor; empty: none). A pull request from a fork keeps
// its source repository in the source_repo_id column schema 33 reserved.
//
//	Compatibility: additive: two columns with defaults, ignored by an older server.
//	Downgrade: nothing to undo.
var forkPullsMigration = migration{additive, `
ALTER TABLE pull_requests ADD COLUMN allow_maintainer_edits INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pull_requests ADD COLUMN run_approved_head TEXT NOT NULL DEFAULT '';
`}
