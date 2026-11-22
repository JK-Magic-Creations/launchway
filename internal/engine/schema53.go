package engine

// Schema 53, requests to look into a delivery (the live tree of its objects,
// manifests, logs, workload controls). It is number 53, after the wall
// between Bases (52).

// schemaInspect is the schema that brought inspect requests.
const schemaInspect = 53

func init() {
	if len(migrations) != schemaInspect-1 {
		panic("the inspect migration (53) must follow migrations 1 to 52")
	}
	migrations = append(migrations, inspectMigration)
}

// 53: inspect_requests holds what a person or tool asked a deploy runner to
//
//	look at (kind tree, manifest, logs or control), who asked, the state
//	(pending, taken, done, failed, canceled), and, once the runner has
//	answered, the answer (result, a JSON document) or why it could not.
//	inspect_chunks holds a followed log's lines as the runner sends them,
//	so that whichever server holds the viewer's connection can read them.
//	Both are short-lived: rows are removed a day after they finish.
//	Compatibility: additive: two new tables, ignored by an older server.
//	Downgrade: nothing to undo.
var inspectMigration = migration{additive, `
CREATE TABLE inspect_requests (
  id            TEXT    PRIMARY KEY,
  target        TEXT    NOT NULL,
  run_id        TEXT    NOT NULL DEFAULT '',
  deployment_id TEXT    NOT NULL DEFAULT '',
  project       TEXT    NOT NULL DEFAULT '',
  kind          TEXT    NOT NULL,
  params        TEXT    NOT NULL DEFAULT '{}',
  actor         TEXT    NOT NULL DEFAULT '',
  state         TEXT    NOT NULL DEFAULT 'pending',
  runner        TEXT    NOT NULL DEFAULT '',
  taken_until   {{BIG}} NOT NULL DEFAULT 0,
  result        TEXT    NOT NULL DEFAULT '',
  error         TEXT    NOT NULL DEFAULT '',
  follow        INTEGER NOT NULL DEFAULT 0,
  seen_at       {{BIG}} NOT NULL DEFAULT 0,
  created_at    {{BIG}} NOT NULL,
  done_at       {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX inspect_requests_open ON inspect_requests(target, state, created_at);
CREATE INDEX inspect_requests_deployment ON inspect_requests(deployment_id, kind, created_at);

CREATE TABLE inspect_chunks (
  request_id TEXT    NOT NULL,
  seq        {{BIG}} NOT NULL,
  body       TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  PRIMARY KEY (request_id, seq)
);
`}
