package engine

// Schema 39, the archive of another server's issues and comments (see
// internal/forge/archive). It is number 39, after delivery (38).

// schemaArchive is the schema that brought the imported-issue archive.
const schemaArchive = 39

func init() {
	if len(migrations) != schemaArchive-1 {
		panic("the issue archive migration (39) must follow migrations 1 to 38")
	}
	migrations = append(migrations, archiveMigration)
}

// 39: imported issues and their comments, a read-only record attached to a
//
//	hosted repository. imported_issues has one row per issue number with its
//	text, state, labels and assignees (JSON lists), times (milliseconds) and
//	where it came from; imported_comments has the comments of each, in the
//	order they had. Nothing here is edited after import; importing a number
//	again replaces it. Compatibility: additive: two new tables, ignored by an
//	older server. Downgrade: nothing to undo.
var archiveMigration = migration{additive, `
CREATE TABLE imported_issues (
  repo_id       TEXT    NOT NULL,
  number        {{BIG}} NOT NULL,
  title         TEXT    NOT NULL DEFAULT '',
  state         TEXT    NOT NULL DEFAULT 'open',
  author        TEXT    NOT NULL DEFAULT '',
  body          TEXT    NOT NULL DEFAULT '',
  labels        TEXT    NOT NULL DEFAULT '[]',
  milestone     TEXT    NOT NULL DEFAULT '',
  assignees     TEXT    NOT NULL DEFAULT '[]',
  url           TEXT    NOT NULL DEFAULT '',
  is_pull       INTEGER NOT NULL DEFAULT 0,
  created_at    {{BIG}} NOT NULL DEFAULT 0,
  updated_at    {{BIG}} NOT NULL DEFAULT 0,
  closed_at     {{BIG}} NOT NULL DEFAULT 0,
  comment_count INTEGER NOT NULL DEFAULT 0,
  source        TEXT    NOT NULL DEFAULT '',
  imported_at   {{BIG}} NOT NULL DEFAULT 0,
  imported_by   TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (repo_id, number)
);
CREATE INDEX imported_issues_state ON imported_issues(repo_id, state, number);

CREATE TABLE imported_comments (
  repo_id      TEXT    NOT NULL,
  issue_number {{BIG}} NOT NULL,
  position     INTEGER NOT NULL,
  source_id    TEXT    NOT NULL DEFAULT '',
  author       TEXT    NOT NULL DEFAULT '',
  body         TEXT    NOT NULL DEFAULT '',
  created_at   {{BIG}} NOT NULL DEFAULT 0,
  updated_at   {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (repo_id, issue_number, position)
);
`}
