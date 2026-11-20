package engine

// Schema 24, flow control, kept in a file of its own: db.go is over the size
// ratchet, and this migration brought a good deal of SQL.
//
// The migrations are appended to the list in db.go when the package loads.
// Schema 23 is reserved for the configuration kept in the database, built on
// another branch: it is an empty stand-in here so that the numbers after it are
// the ones their branches were given. When that branch is merged, its real
// migration 23 takes the stand-in's place in db.go and migration23Stand is
// deleted with its line in init.

// schemaFlow is the schema that brought flow control: conditions on step
// outputs, tolerated failures, retry waits and causes, run timeouts, lanes,
// the target branch of pull requests and re-running from several steps.
// Schema 23 is the configuration kept in the database, built elsewhere.
const schemaFlow = 24

// 23: reserved for the configuration kept in the database (built on
//
//	another branch). It is an empty stand-in here so that the numbers
//	after it are the ones their branches were given; the migration
//	tests accept it, and only it, being empty.
//	Compatibility: additive: nothing.
var migration23Stand = migration{additive, ``}

// 24: flow control (see flow.go in the pipeline package): conditions on
//
//	step outputs, tolerated failures, retry waits and causes, a run
//	timeout, lanes, the target branch of a pull request, and
//	re-running from several steps. A step that failed and was allowed
//	to (on_failure: continue) is marked in steps.allowed so that every
//	view of the run, including live updates, can say so. A retry that
//	waits is queued with steps.not_before, which leasing respects, and
//	a failed attempt's cause is kept in steps.fail_cause. A run's
//	timeout is kept as the time it runs out, runs.deadline_at, worked
//	out from its events (0: none, or paused while an approval waits)
//	so that every server's reaper finds the runs that are due in SQL.
//	installation_limits holds the installation's limits on pipelines:
//	for now the longest run timeout one may ask for. Lanes (run-level
//	concurrency) are derived from the run.lane_* events like
//	deploy_locks and deploy_queue are from the deploy events:
//	lane_holders has the run that holds each lane and lane_waiting the
//	runs queued behind it, and runs.lane_* says which lane a run is in
//	and how it stands in it. A pull-request run records the branch it
//	targets in runs.pull_request_into, when the project's forge said. A
//	re-run from several steps lists them as JSON in runs.rerun_froms
//	(rerun_from keeps the first).
//	Compatibility: additive: new columns with defaults and, in the
//	lanes' case, new tables. Pipelines that use the new keys carry
//	"requires": 24 and the events that use new fields carry
//	Requires, so older servers leave such runs alone.
var migrationFlow = migration{additive, `
ALTER TABLE steps ADD COLUMN allowed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN not_before {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN fail_cause TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN deadline_at {{BIG}} NOT NULL DEFAULT 0;
CREATE INDEX runs_deadline ON runs(deadline_at) WHERE deadline_at > 0;

CREATE TABLE installation_limits (
  name           TEXT PRIMARY KEY,
  max_run_timeout {{BIG}} NOT NULL DEFAULT 0,
  updated_at     {{BIG}} NOT NULL DEFAULT 0,
  updated_by     TEXT    NOT NULL DEFAULT ''
);

ALTER TABLE runs ADD COLUMN lane_name TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN lane_key TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN lane_label TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN lane_state TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN lane_other TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN lane_other_number INTEGER NOT NULL DEFAULT 0;
CREATE INDEX runs_lane ON runs(project, lane_name);
ALTER TABLE runs ADD COLUMN pull_request_into TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN rerun_froms TEXT NOT NULL DEFAULT '';

CREATE TABLE lane_holders (
  project  TEXT    NOT NULL,
  lane_key TEXT    NOT NULL,
  run_id   TEXT    NOT NULL,
  since    {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, lane_key)
);

CREATE TABLE lane_waiting (
  run_id   TEXT PRIMARY KEY,
  project  TEXT    NOT NULL,
  lane_key TEXT    NOT NULL,
  position INTEGER NOT NULL,
  since    {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX lane_waiting_lane ON lane_waiting(project, lane_key, position);
`}

func init() {
	migrations = append(migrations, migration23Stand, migrationFlow)
}
