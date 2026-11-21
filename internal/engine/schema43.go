package engine

// Schema 43, pull-request review depth (internal/forge/pulls): code owners,
// stricter branch rules, files marked viewed and the merge queue. It is
// number 43, after the event log (42).

// schemaReview is the schema that brought them.
const schemaReview = 43

func init() {
	if len(migrations) != schemaReview-1 {
		panic("the review migration (43) must follow migrations 1 to 42")
	}
	migrations = append(migrations, reviewMigration)
}

// 43: branch_rules.extra holds the rules that came after the first set (a JSON
//
//	object: owner approval, approval of the latest push by someone else,
//	required checks by path, the merge queue), so an older server that
//	ignores the column still reads the rules it knows. pull_viewed records
//	which file of a pull request a person marked viewed, at which blob, so a
//	file that changes is no longer viewed. pull_queue is the merge queue:
//	one row per queued pull request, in order (seq). A group asked to review
//	is a pull_people row of kind 'reviewer_group'. pull_reviews.reviewer_groups records the sign-in groups of the reviewer when the
//	review was submitted, so a review can satisfy a request for a group.
//	Compatibility: additive: new columns with defaults and new tables, ignored
//	by an older server. Downgrade: nothing to undo.
var reviewMigration = migration{additive, `
ALTER TABLE branch_rules ADD COLUMN extra TEXT NOT NULL DEFAULT '{}';
ALTER TABLE repo_pull_settings ADD COLUMN extra TEXT NOT NULL DEFAULT '{}';
ALTER TABLE pull_reviews ADD COLUMN reviewer_groups TEXT NOT NULL DEFAULT '[]';

CREATE TABLE pull_viewed (
  pull_id   TEXT    NOT NULL,
  person    TEXT    NOT NULL,
  path      TEXT    NOT NULL,
  blob      TEXT    NOT NULL DEFAULT '',
  viewed_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (pull_id, person, path)
);

CREATE TABLE pull_queue (
  seq        {{ID}},
  pull_id    TEXT    NOT NULL UNIQUE,
  repo_id    TEXT    NOT NULL,
  branch     TEXT    NOT NULL,
  method     TEXT    NOT NULL DEFAULT 'merge',
  queued_by  TEXT    NOT NULL DEFAULT '',
  queued_at  {{BIG}} NOT NULL DEFAULT 0,
  priority   INTEGER NOT NULL DEFAULT 0,
  state      TEXT    NOT NULL DEFAULT 'waiting',
  head_sha   TEXT    NOT NULL DEFAULT '',
  tested_sha TEXT    NOT NULL DEFAULT '',
  run_ids    TEXT    NOT NULL DEFAULT '[]',
  reason     TEXT    NOT NULL DEFAULT '',
  updated_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX pull_queue_branch ON pull_queue(repo_id, branch, seq);
`}
