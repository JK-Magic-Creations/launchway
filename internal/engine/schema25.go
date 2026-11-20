package engine

// 23 and 24 belong to other feature branches; db.go carries them empty so
// that 25 keeps its number. When those branches merge, their migrations take
// those places. A database must never be migrated by a build that has the
// placeholders and then by one with the real 23 and 24: only test databases
// are.
//
// 25: build caches (see cache.go). A step may keep a directory between
//
//	runs (the pipeline key cache). An entry is one archive in the blob
//	store, under caches/, named by project, scope (default, ref:<ref>
//	or pull:<n>, worked out by the server from the run, never stated
//	by a runner), name, key and platform; entries are immutable. The
//	unique index covers entries that are not ended, so that an ended
//	entry's name can be saved again while its bytes wait to be
//	removed. cache_usage counts the bytes of each project's kept
//	entries, like artifact_usage. cache_policy holds the limits: the
//	row of project '' is the installation's, any other row overrides
//	it for that project; with no row at all the values compiled in
//	apply.
//	Compatibility: additive: new tables and nothing else, and no new
//	event type (step.finished may carry caches, which older servers
//	ignore, as they ignore every field they do not know). Older
//	servers do not know the pipeline key, so a pipeline that uses it
//	carries "requires": 25 and they leave such runs alone.
const schema25SQL = `
CREATE TABLE caches (
  id              TEXT    PRIMARY KEY,
  project         TEXT    NOT NULL,
  scope           TEXT    NOT NULL,
  name            TEXT    NOT NULL,
  key             TEXT    NOT NULL,
  platform        TEXT    NOT NULL,
  blob_key        TEXT    NOT NULL,
  store           TEXT    NOT NULL DEFAULT '',
  sha256          TEXT    NOT NULL DEFAULT '',
  bytes           {{BIG}} NOT NULL DEFAULT 0,
  files           INTEGER NOT NULL DEFAULT 0,
  created_run     TEXT    NOT NULL DEFAULT '',
  created_step    TEXT    NOT NULL DEFAULT '',
  created_attempt INTEGER NOT NULL DEFAULT 0,
  created_at      {{BIG}} NOT NULL DEFAULT 0,
  last_used_at    {{BIG}} NOT NULL DEFAULT 0,
  hits            {{BIG}} NOT NULL DEFAULT 0,
  status          TEXT    NOT NULL,
  ended_at        {{BIG}} NOT NULL DEFAULT 0,
  ended_by        TEXT    NOT NULL DEFAULT '',
  upload_id       TEXT    NOT NULL DEFAULT '',
  received        {{BIG}} NOT NULL DEFAULT 0,
  hash_state      TEXT    NOT NULL DEFAULT '',
  parts           TEXT    NOT NULL DEFAULT '[]'
);
CREATE UNIQUE INDEX caches_entry ON caches(project, scope, name, key, platform) WHERE status IN ('uploading', 'kept');
CREATE INDEX caches_project ON caches(project, status, last_used_at);
CREATE INDEX caches_status ON caches(status, last_used_at);
CREATE INDEX caches_blob ON caches(blob_key);

CREATE TABLE cache_usage (
  project     TEXT    PRIMARY KEY,
  bytes       {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE cache_policy (
  project           TEXT    PRIMARY KEY,
  enabled           INTEGER NOT NULL DEFAULT 1,
  max_project_bytes {{BIG}} NOT NULL DEFAULT 0,
  max_entry_bytes   {{BIG}} NOT NULL DEFAULT 0,
  idle_days         INTEGER NOT NULL DEFAULT 0,
  updated_at        {{BIG}} NOT NULL DEFAULT 0,
  updated_by        TEXT    NOT NULL DEFAULT ''
);
`
