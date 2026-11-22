package engine

// Schema 67: what the repository Overview keeps (see docs/guide/repositories.md): the languages of a repository's
// default branch and the contributors job, each keyed by the head commit they were made for. It is number 67, after
// the repository trash (66).

// schemaOverview is the schema that brought them.
const schemaOverview = 67

func init() {
	if len(migrations) != schemaOverview-1 {
		panic("migration 67 must follow migrations 1 to 66")
	}
	migrations = append(migrations, overviewMigration)
}

// 67: repo_languages holds one answer per repository, made for the commit named (a different head makes a new one).
// repo_contributors holds one row per repository and period: the job's state (pending, running, done, failed), the
// commit it was or is made for and, when done, the answer as JSON. Nothing in either is a source of truth: both can be
// deleted and are made again.
//
//	Compatibility: additive: two new tables, ignored by an older server.
//	Downgrade: nothing to undo.
var overviewMigration = migration{additive, `
CREATE TABLE repo_languages (
  repo_id     TEXT PRIMARY KEY,
  commit_sha  TEXT    NOT NULL,
  data        TEXT    NOT NULL DEFAULT '{}',
  computed_at {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE repo_contributors (
  repo_id      TEXT NOT NULL,
  period       TEXT NOT NULL,
  commit_sha   TEXT    NOT NULL,
  state        TEXT    NOT NULL DEFAULT 'pending',
  data         TEXT    NOT NULL DEFAULT '{}',
  requested_at {{BIG}} NOT NULL DEFAULT 0,
  computed_at  {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_id, period)
);
`}
