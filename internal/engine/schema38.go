package engine

// Schema 38, queues and limits: how many runs of a kind go at once, in what
// order the others wait, and why a step that cannot start for lack of room
// waits. Kept in files of their own (schema38.go and queues*.go): db.go is
// over the size ratchet.

// schemaQueues is the schema that brought queues and limits.
const schemaQueues = 38

func init() {
	if len(migrations) != schemaQueues-1 {
		panic("the queues migration (38) must follow migrations 1 to 37")
	}
	migrations = append(migrations, queuesMigration)
}

// 38: queues and limits.
//
//	run_queue has one row for each run that counts against a limit, from its
//	creation until it ends: state held (waiting to be let go: not one of its
//	steps is queued) or active (let go). class is its kind (push_protected,
//	push, pull_request, schedule or manual) and author_class says whether
//	automation started it, so that limits and the order of the queue can be
//	worked out in the database. priority is what the order goes by (higher
//	first) and bumped is when an operator asked for the run to go next.
//	step_waits has one row for a queued step that waits for room (the step
//	namespace's quota, no free runner): the stable code, the sentence, when
//	it first waited and how many times it tried again.
//	Compatibility: additive: two new tables. Nothing older code reads
//	changes; a run held in the queue carries the schema in its minimum, so
//	an older server leaves it alone.
var queuesMigration = migration{additive, `
CREATE TABLE run_queue (
  run_id       TEXT PRIMARY KEY,
  project      TEXT    NOT NULL,
  pipeline     TEXT    NOT NULL DEFAULT '',
  class        TEXT    NOT NULL,
  author_class TEXT    NOT NULL DEFAULT '',
  state        TEXT    NOT NULL DEFAULT 'active',
  manual       INTEGER NOT NULL DEFAULT 0,
  priority     INTEGER NOT NULL DEFAULT 0,
  bumped       {{BIG}} NOT NULL DEFAULT 0,
  since        {{BIG}} NOT NULL DEFAULT 0,
  code         TEXT    NOT NULL DEFAULT '',
  reason       TEXT    NOT NULL DEFAULT '',
  behind       TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX run_queue_project ON run_queue(project, state, since);
CREATE TABLE step_waits (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  code        TEXT    NOT NULL,
  reason      TEXT    NOT NULL DEFAULT '',
  since       {{BIG}} NOT NULL,
  first_since {{BIG}} NOT NULL,
  tries       INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (run_id, step_id)
);
`}
