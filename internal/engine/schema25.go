package engine

// The migration is appended after 23 (configuration in the database, db.go)
// and 24 (flow control, migrations_flow.go), which init in migrations_flow.go
// has already appended: Go runs a package's init functions in the order of the
// file names, and the check below fails the start if the list is not 24 long.
//
// TODO: fold the cache_policy table below into configuration objects (a kind
// of internal/config), so cache limits are versioned, audited and managed like
// every other setting.
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

func init() {
	if len(migrations) != schemaCache-1 {
		panic("the build caches migration (25) must follow migrations 1 to 24")
	}
	migrations = append(migrations, migration{additive, schema25SQL})
}
