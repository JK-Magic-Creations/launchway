package engine

// Schema 56, work tracking: issues, stories and epics with a workflow,
// planning (boards, backlog order, sprints, milestones) and agents that may be
// assigned work (internal/work). Migration 55 and the ones before it that are
// not merged yet are empty stand-ins (schema40_standins.go).

// schemaWork is the schema that brought work tracking.
const schemaWork = 56

func init() {
	if len(migrations) != schemaWork-1 {
		panic("the work tracking migration (56) must follow migrations 1 to 55")
	}
	migrations = append(migrations, workMigration)
}

// 56: work tracking. Every table is new, so an older server ignores them.
// Money and time are whole numbers (estimates in thousandths of a point or of
// an hour, times in milliseconds); lists that are only read back whole are
// JSON text, lists that are searched have a table of their own.
// Compatibility: additive. Downgrade: nothing to undo.
var workMigration = migration{additive, `
CREATE TABLE work_scopes (
  scope_kind  TEXT    NOT NULL,
  scope_id    TEXT    NOT NULL,
  prefix      TEXT    NOT NULL DEFAULT '',
  next_number {{BIG}} NOT NULL DEFAULT 1,
  settings    TEXT    NOT NULL DEFAULT '{}',
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (scope_kind, scope_id)
);
CREATE UNIQUE INDEX work_scopes_prefix ON work_scopes(prefix) WHERE prefix <> '';

CREATE TABLE work_items (
  id             TEXT    NOT NULL PRIMARY KEY,
  scope_kind     TEXT    NOT NULL,
  scope_id       TEXT    NOT NULL,
  number         {{BIG}} NOT NULL,
  type           TEXT    NOT NULL DEFAULT 'issue',
  title          TEXT    NOT NULL,
  body           TEXT    NOT NULL DEFAULT '',
  state          TEXT    NOT NULL DEFAULT 'backlog',
  resolution     TEXT    NOT NULL DEFAULT '',
  priority       TEXT    NOT NULL DEFAULT 'normal',
  estimate_milli {{BIG}} NOT NULL DEFAULT 0,
  parent_id      TEXT    NOT NULL DEFAULT '',
  milestone_id   TEXT    NOT NULL DEFAULT '',
  sprint_id      TEXT    NOT NULL DEFAULT '',
  rank           {{BIG}} NOT NULL DEFAULT 0,
  start_at       {{BIG}} NOT NULL DEFAULT 0,
  due_at         {{BIG}} NOT NULL DEFAULT 0,
  reporter       TEXT    NOT NULL DEFAULT '',
  reporter_kind  TEXT    NOT NULL DEFAULT 'person',
  confidential   INTEGER NOT NULL DEFAULT 0,
  difficulty     TEXT    NOT NULL DEFAULT '',
  custom         TEXT    NOT NULL DEFAULT '{}',
  source_key     TEXT    NOT NULL DEFAULT '',
  version        INTEGER NOT NULL DEFAULT 1,
  created_at     {{BIG}} NOT NULL DEFAULT 0,
  updated_at     {{BIG}} NOT NULL DEFAULT 0,
  closed_at      {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX work_items_number ON work_items(scope_kind, scope_id, number);
CREATE INDEX work_items_rank ON work_items(scope_kind, scope_id, rank);
CREATE INDEX work_items_state ON work_items(scope_kind, scope_id, state);
CREATE INDEX work_items_parent ON work_items(parent_id);
CREATE INDEX work_items_sprint ON work_items(sprint_id);
CREATE INDEX work_items_milestone ON work_items(milestone_id);
CREATE INDEX work_items_updated ON work_items(updated_at);
CREATE INDEX work_items_source ON work_items(scope_kind, scope_id, source_key);

CREATE TABLE work_labels (
  item_id TEXT NOT NULL,
  label   TEXT NOT NULL,
  PRIMARY KEY (item_id, label)
);
CREATE INDEX work_labels_label ON work_labels(label);

CREATE TABLE work_assignees (
  item_id TEXT NOT NULL,
  who     TEXT NOT NULL,
  kind    TEXT NOT NULL DEFAULT 'person',
  PRIMARY KEY (item_id, who)
);
CREATE INDEX work_assignees_who ON work_assignees(who, kind);

CREATE TABLE work_watchers (
  item_id TEXT NOT NULL,
  who     TEXT NOT NULL,
  PRIMARY KEY (item_id, who)
);

CREATE TABLE work_links (
  id         TEXT    NOT NULL PRIMARY KEY,
  from_id    TEXT    NOT NULL,
  to_id      TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  created_by TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX work_links_pair ON work_links(from_id, to_id, kind);
CREATE INDEX work_links_to ON work_links(to_id, kind);

CREATE TABLE work_code_links (
  id         TEXT    NOT NULL PRIMARY KEY,
  item_id    TEXT    NOT NULL,
  kind       TEXT    NOT NULL,
  repo       TEXT    NOT NULL DEFAULT '',
  ref        TEXT    NOT NULL,
  title      TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT '',
  closes     INTEGER NOT NULL DEFAULT 0,
  source     TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL DEFAULT 0,
  updated_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX work_code_links_unique ON work_code_links(item_id, kind, repo, ref);
CREATE INDEX work_code_links_ref ON work_code_links(kind, repo, ref);

CREATE TABLE work_comments (
  id          TEXT    NOT NULL PRIMARY KEY,
  item_id     TEXT    NOT NULL,
  author      TEXT    NOT NULL DEFAULT '',
  author_kind TEXT    NOT NULL DEFAULT 'person',
  body        TEXT    NOT NULL DEFAULT '',
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  edited_at   {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX work_comments_item ON work_comments(item_id, created_at);

CREATE TABLE work_comment_edits (
  comment_id TEXT    NOT NULL,
  rev        INTEGER NOT NULL,
  body       TEXT    NOT NULL DEFAULT '',
  edited_by  TEXT    NOT NULL DEFAULT '',
  at         {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (comment_id, rev)
);

CREATE TABLE work_events (
  seq        {{ID}},
  item_id    TEXT    NOT NULL,
  at         {{BIG}} NOT NULL,
  actor      TEXT    NOT NULL DEFAULT '',
  actor_kind TEXT    NOT NULL DEFAULT 'person',
  via        TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,
  data       TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX work_events_item ON work_events(item_id, seq);
CREATE INDEX work_events_kind ON work_events(kind, at);

CREATE TABLE work_criteria (
  id      TEXT    NOT NULL PRIMARY KEY,
  item_id TEXT    NOT NULL,
  pos     INTEGER NOT NULL DEFAULT 0,
  text    TEXT    NOT NULL DEFAULT '',
  done    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX work_criteria_item ON work_criteria(item_id, pos);

CREATE TABLE work_milestones (
  id          TEXT    NOT NULL PRIMARY KEY,
  scope_kind  TEXT    NOT NULL,
  scope_id    TEXT    NOT NULL,
  title       TEXT    NOT NULL,
  description TEXT    NOT NULL DEFAULT '',
  due_at      {{BIG}} NOT NULL DEFAULT 0,
  state       TEXT    NOT NULL DEFAULT 'open',
  created_at  {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX work_milestones_title ON work_milestones(scope_kind, scope_id, title);

CREATE TABLE work_sprints (
  id          TEXT    NOT NULL PRIMARY KEY,
  scope_kind  TEXT    NOT NULL,
  scope_id    TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  goal        TEXT    NOT NULL DEFAULT '',
  start_at    {{BIG}} NOT NULL DEFAULT 0,
  end_at      {{BIG}} NOT NULL DEFAULT 0,
  state       TEXT    NOT NULL DEFAULT 'planned',
  capacity    TEXT    NOT NULL DEFAULT '{}',
  started_at  {{BIG}} NOT NULL DEFAULT 0,
  completed_at {{BIG}} NOT NULL DEFAULT 0,
  created_at  {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX work_sprints_name ON work_sprints(scope_kind, scope_id, name);

CREATE TABLE work_sprint_items (
  sprint_id      TEXT    NOT NULL,
  item_id        TEXT    NOT NULL,
  estimate_milli {{BIG}} NOT NULL DEFAULT 0,
  added_at       {{BIG}} NOT NULL DEFAULT 0,
  committed      INTEGER NOT NULL DEFAULT 0,
  completed      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (sprint_id, item_id)
);

CREATE TABLE work_attachments (
  id           TEXT    NOT NULL PRIMARY KEY,
  item_id      TEXT    NOT NULL,
  name         TEXT    NOT NULL,
  content_type TEXT    NOT NULL DEFAULT 'application/octet-stream',
  size         {{BIG}} NOT NULL DEFAULT 0,
  sha256       TEXT    NOT NULL DEFAULT '',
  kind         TEXT    NOT NULL DEFAULT 'file',
  added_by     TEXT    NOT NULL DEFAULT '',
  at           {{BIG}} NOT NULL DEFAULT 0,
  data         {{BYTES}}
);
CREATE INDEX work_attachments_item ON work_attachments(item_id);

CREATE TABLE work_views (
  id         TEXT    NOT NULL PRIMARY KEY,
  scope_kind TEXT    NOT NULL,
  scope_id   TEXT    NOT NULL,
  owner      TEXT    NOT NULL DEFAULT '',
  name       TEXT    NOT NULL,
  query      TEXT    NOT NULL DEFAULT '',
  shared     INTEGER NOT NULL DEFAULT 0,
  created_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX work_views_name ON work_views(scope_kind, scope_id, owner, name);

CREATE TABLE work_agents (
  id            TEXT    NOT NULL PRIMARY KEY,
  name          TEXT    NOT NULL,
  scope_kind    TEXT    NOT NULL DEFAULT 'installation',
  scope_id      TEXT    NOT NULL DEFAULT '',
  description   TEXT    NOT NULL DEFAULT '',
  owner         TEXT    NOT NULL DEFAULT '',
  projects      TEXT    NOT NULL DEFAULT '[]',
  types         TEXT    NOT NULL DEFAULT '[]',
  labels        TEXT    NOT NULL DEFAULT '[]',
  concurrency   INTEGER NOT NULL DEFAULT 1,
  per_day       INTEGER NOT NULL DEFAULT 10,
  may_merge     INTEGER NOT NULL DEFAULT 0,
  model         TEXT    NOT NULL DEFAULT '',
  class         TEXT    NOT NULL DEFAULT '',
  budget_tokens_day {{BIG}} NOT NULL DEFAULT 0,
  budget_cents_day  {{BIG}} NOT NULL DEFAULT 0,
  item_tokens       {{BIG}} NOT NULL DEFAULT 0,
  item_cents        {{BIG}} NOT NULL DEFAULT 0,
  item_seconds      {{BIG}} NOT NULL DEFAULT 0,
  disabled      INTEGER NOT NULL DEFAULT 0,
  token_hash    TEXT    NOT NULL DEFAULT '',
  token_hint    TEXT    NOT NULL DEFAULT '',
  token_at      {{BIG}} NOT NULL DEFAULT 0,
  last_seen     {{BIG}} NOT NULL DEFAULT 0,
  created_at    {{BIG}} NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX work_agents_name ON work_agents(name);
CREATE INDEX work_agents_token ON work_agents(token_hash);

CREATE TABLE work_claims (
  item_id      TEXT    NOT NULL PRIMARY KEY,
  agent        TEXT    NOT NULL,
  claimed_at   {{BIG}} NOT NULL,
  heartbeat_at {{BIG}} NOT NULL,
  lease_until  {{BIG}} NOT NULL,
  note         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX work_claims_agent ON work_claims(agent);

CREATE TABLE work_inbox (
  id      {{ID}},
  who     TEXT    NOT NULL,
  kind    TEXT    NOT NULL,
  item_id TEXT    NOT NULL DEFAULT '',
  text    TEXT    NOT NULL DEFAULT '',
  at      {{BIG}} NOT NULL,
  read_at {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX work_inbox_who ON work_inbox(who, read_at, id);

CREATE TABLE work_agent_log (
  seq     {{ID}},
  agent   TEXT    NOT NULL,
  at      {{BIG}} NOT NULL,
  item_id TEXT    NOT NULL DEFAULT '',
  action  TEXT    NOT NULL,
  detail  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX work_agent_log_agent ON work_agent_log(agent, at);

CREATE TABLE work_usage (
  seq     {{ID}},
  agent   TEXT    NOT NULL,
  item_id TEXT    NOT NULL,
  at      {{BIG}} NOT NULL,
  tokens  {{BIG}} NOT NULL DEFAULT 0,
  cents   {{BIG}} NOT NULL DEFAULT 0,
  seconds {{BIG}} NOT NULL DEFAULT 0,
  note    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX work_usage_agent ON work_usage(agent, at);
CREATE INDEX work_usage_item ON work_usage(item_id);

CREATE TABLE work_agent_changes (
  seq        {{ID}},
  scope_kind TEXT    NOT NULL,
  scope_id   TEXT    NOT NULL,
  agent      TEXT    NOT NULL,
  repo       TEXT    NOT NULL DEFAULT '',
  pull       TEXT    NOT NULL DEFAULT '',
  outcome    TEXT    NOT NULL,
  at         {{BIG}} NOT NULL
);
CREATE INDEX work_agent_changes_scope ON work_agent_changes(scope_kind, scope_id, at);

CREATE TABLE work_autonomy_state (
  scope_kind TEXT    NOT NULL,
  scope_id   TEXT    NOT NULL,
  cleared_at {{BIG}} NOT NULL DEFAULT 0,
  cleared_by TEXT    NOT NULL DEFAULT '',
  reason     TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (scope_kind, scope_id)
);

CREATE TABLE work_agent_reviews (
  id         TEXT    NOT NULL PRIMARY KEY,
  scope_kind TEXT    NOT NULL,
  scope_id   TEXT    NOT NULL,
  repo       TEXT    NOT NULL,
  pull       TEXT    NOT NULL,
  agent      TEXT    NOT NULL,
  verdict    TEXT    NOT NULL,
  counts     INTEGER NOT NULL DEFAULT 0,
  detail     TEXT    NOT NULL DEFAULT '{}',
  advice     TEXT    NOT NULL DEFAULT '',
  at         {{BIG}} NOT NULL
);
CREATE INDEX work_agent_reviews_pull ON work_agent_reviews(repo, pull);

CREATE TABLE work_review_samples (
  id          TEXT    NOT NULL PRIMARY KEY,
  scope_kind  TEXT    NOT NULL,
  scope_id    TEXT    NOT NULL,
  repo        TEXT    NOT NULL,
  pull        TEXT    NOT NULL,
  agent       TEXT    NOT NULL DEFAULT '',
  flagged_at  {{BIG}} NOT NULL,
  reviewed_by TEXT    NOT NULL DEFAULT '',
  reviewed_at {{BIG}} NOT NULL DEFAULT 0,
  note        TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX work_review_samples_pull ON work_review_samples(repo, pull);
`}
