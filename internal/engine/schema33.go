package engine

// Schema 33, pull requests and code review of hosted repositories
// (internal/forge/pulls and internal/api/forge_pulls*.go), kept in a file of
// its own: db.go is over the size ratchet. It is named so that its init runs
// after schema32.go's.

// schemaPulls is the schema that brought pull requests.
const schemaPulls = 33

func init() {
	if len(migrations) != schemaPulls-1 {
		panic("the pull request migration (33) must follow migrations 1 to 32")
	}
	migrations = append(migrations, pullsMigration)
}

// 33: pull requests.
//
//	pull_requests holds one row per pull request, numbered per repository
//	(pull_counters hands the numbers out inside a transaction). The columns
//	merge_* are the server's last judgement of whether it can merge, with
//	the head and base it judged, so a stale judgement is recognisable.
//	source_repo_id is reserved for pull requests from a fork (empty: the
//	same repository). pull_people lists assignees and requested reviewers.
//	pull_revisions records the head and base at every update, so history
//	survives a force-push; pull_runs links the runs of a pull request.
//	pull_comments holds conversation comments and review comments (anchored
//	to a line of a file at a commit, grouped in threads); pull_reviews the
//	verdicts. repo_pull_settings is the per-repository merge settings.
//	branch_rules gains the merge rules of the target branch.
//	Compatibility: additive: new tables and new columns with defaults.
//	Nothing older code reads changes.
//	Downgrade: nothing to undo for an older server; the tables and columns
//	stay and are ignored.
var pullsMigration = migration{additive, `
CREATE TABLE pull_counters (
  repo_id     TEXT PRIMARY KEY,
  next_number {{BIG}} NOT NULL DEFAULT 1
);

CREATE TABLE pull_requests (
  id                 TEXT PRIMARY KEY,
  repo_id            TEXT    NOT NULL,
  number             {{BIG}} NOT NULL,
  title              TEXT    NOT NULL,
  body               TEXT    NOT NULL DEFAULT '',
  author             TEXT    NOT NULL,
  source_repo_id     TEXT    NOT NULL DEFAULT '',
  source_branch      TEXT    NOT NULL,
  target_branch      TEXT    NOT NULL,
  state              TEXT    NOT NULL DEFAULT 'open',
  draft              INTEGER NOT NULL DEFAULT 0,
  labels             TEXT    NOT NULL DEFAULT '[]',
  head_sha           TEXT    NOT NULL DEFAULT '',
  base_sha           TEXT    NOT NULL DEFAULT '',
  merge_base         TEXT    NOT NULL DEFAULT '',
  merge_state        TEXT    NOT NULL DEFAULT 'unknown',
  merge_files        TEXT    NOT NULL DEFAULT '[]',
  merge_behind       {{BIG}} NOT NULL DEFAULT 0,
  merge_ahead        {{BIG}} NOT NULL DEFAULT 0,
  merge_reason       TEXT    NOT NULL DEFAULT '',
  merge_checked_head TEXT    NOT NULL DEFAULT '',
  merge_checked_base TEXT    NOT NULL DEFAULT '',
  merge_checked_at   {{BIG}} NOT NULL DEFAULT 0,
  merge_commit       TEXT    NOT NULL DEFAULT '',
  merge_method       TEXT    NOT NULL DEFAULT '',
  merged_by          TEXT    NOT NULL DEFAULT '',
  reverts            {{BIG}} NOT NULL DEFAULT 0,
  auto_merge         TEXT    NOT NULL DEFAULT '',
  auto_merge_by      TEXT    NOT NULL DEFAULT '',
  created_at         {{BIG}} NOT NULL DEFAULT 0,
  updated_at         {{BIG}} NOT NULL DEFAULT 0,
  merged_at          {{BIG}} NOT NULL DEFAULT 0,
  closed_at          {{BIG}} NOT NULL DEFAULT 0,
  closed_by          TEXT    NOT NULL DEFAULT '',
  UNIQUE (repo_id, number)
);
CREATE INDEX pull_requests_by_repo ON pull_requests (repo_id, state, number);
CREATE INDEX pull_requests_by_author ON pull_requests (author);
CREATE INDEX pull_requests_by_branch ON pull_requests (repo_id, source_branch, state);

CREATE TABLE pull_people (
  pull_id  TEXT    NOT NULL,
  kind     TEXT    NOT NULL,
  name     TEXT    NOT NULL,
  added_by TEXT    NOT NULL DEFAULT '',
  added_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (pull_id, kind, name)
);
CREATE INDEX pull_people_by_name ON pull_people (kind, name);

CREATE TABLE pull_revisions (
  pull_id    TEXT    NOT NULL,
  seq        {{BIG}} NOT NULL,
  head_sha   TEXT    NOT NULL,
  base_sha   TEXT    NOT NULL,
  forced     INTEGER NOT NULL DEFAULT 0,
  actor      TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (pull_id, seq)
);

CREATE TABLE pull_runs (
  pull_id    TEXT    NOT NULL,
  run_id     TEXT    NOT NULL,
  head_sha   TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (pull_id, run_id)
);

CREATE TABLE pull_reviews (
  id               TEXT PRIMARY KEY,
  pull_id          TEXT    NOT NULL,
  author           TEXT    NOT NULL,
  verdict          TEXT    NOT NULL,
  body             TEXT    NOT NULL DEFAULT '',
  commit_sha       TEXT    NOT NULL DEFAULT '',
  author_role      TEXT    NOT NULL DEFAULT '',
  state            TEXT    NOT NULL DEFAULT 'submitted',
  dismissed_by     TEXT    NOT NULL DEFAULT '',
  dismissed_reason TEXT    NOT NULL DEFAULT '',
  created_at       {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX pull_reviews_by_pull ON pull_reviews (pull_id, created_at);

CREATE TABLE pull_comments (
  id          TEXT PRIMARY KEY,
  pull_id     TEXT    NOT NULL,
  review_id   TEXT    NOT NULL DEFAULT '',
  thread_id   TEXT    NOT NULL DEFAULT '',
  author      TEXT    NOT NULL,
  body        TEXT    NOT NULL,
  path        TEXT    NOT NULL DEFAULT '',
  commit_sha  TEXT    NOT NULL DEFAULT '',
  side        TEXT    NOT NULL DEFAULT 'new',
  start_line  {{BIG}} NOT NULL DEFAULT 0,
  line        {{BIG}} NOT NULL DEFAULT 0,
  orig_line   {{BIG}} NOT NULL DEFAULT 0,
  suggestion  TEXT    NOT NULL DEFAULT '',
  has_suggestion INTEGER NOT NULL DEFAULT 0,
  applied_sha TEXT    NOT NULL DEFAULT '',
  outdated    INTEGER NOT NULL DEFAULT 0,
  resolved    INTEGER NOT NULL DEFAULT 0,
  resolved_by TEXT    NOT NULL DEFAULT '',
  deleted     INTEGER NOT NULL DEFAULT 0,
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_at  {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX pull_comments_by_pull ON pull_comments (pull_id, created_at);
CREATE INDEX pull_comments_by_thread ON pull_comments (thread_id);

CREATE TABLE repo_pull_settings (
  repo_id         TEXT PRIMARY KEY,
  allow_merge     INTEGER NOT NULL DEFAULT 1,
  allow_squash    INTEGER NOT NULL DEFAULT 1,
  allow_rebase    INTEGER NOT NULL DEFAULT 1,
  default_method  TEXT    NOT NULL DEFAULT 'merge',
  delete_branch   INTEGER NOT NULL DEFAULT 0,
  dismiss_stale   INTEGER NOT NULL DEFAULT 1,
  merge_template  TEXT    NOT NULL DEFAULT '',
  squash_template TEXT    NOT NULL DEFAULT '',
  updated_at      {{BIG}} NOT NULL DEFAULT 0,
  updated_by      TEXT    NOT NULL DEFAULT ''
);

ALTER TABLE branch_rules ADD COLUMN required_approvals {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE branch_rules ADD COLUMN approval_role TEXT NOT NULL DEFAULT 'operator';
ALTER TABLE branch_rules ADD COLUMN required_checks TEXT NOT NULL DEFAULT '[]';
ALTER TABLE branch_rules ADD COLUMN require_up_to_date INTEGER NOT NULL DEFAULT 0;
ALTER TABLE branch_rules ADD COLUMN require_resolved INTEGER NOT NULL DEFAULT 0;
ALTER TABLE branch_rules ADD COLUMN no_self_approval INTEGER NOT NULL DEFAULT 1;
ALTER TABLE branch_rules ADD COLUMN require_signed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE branch_rules ADD COLUMN bypass_admins INTEGER NOT NULL DEFAULT 0;
`}
