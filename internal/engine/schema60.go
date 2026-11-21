package engine

// Schema 60, requests to look into a delivery (the live tree of its objects,
// manifests, logs, workload controls). Numbers 40 to 59 belong to branches
// that have not merged; until they do this build holds empty stand-ins, so
// that this migration has the number it will have when all are merged.
// The migration-order tests allow exactly these stand-ins; when a real
// number merges, replace its stand-in with it.

// schemaInspect is the schema that brought inspect requests.
const schemaInspect = 60

// firstStandIn and lastStandIn bound the numbers held empty.
const (
	firstStandIn = 40
	lastStandIn  = 59
)

func init() {
	if len(migrations) != firstStandIn-1 {
		panic("the stand-ins before migration 60 must follow migrations 1 to 39")
	}
	for i := firstStandIn; i <= lastStandIn; i++ {
		migrations = append(migrations, migration{additive, ``})
	}
	migrations = append(migrations, inspectMigration)
}

// 60: inspect_requests holds what a person or tool asked a deploy runner to
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
