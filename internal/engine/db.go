package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL, for running several servers
	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/store"
	_ "modernc.org/sqlite" // pure Go SQLite, for a single server
)

// dbx wraps a database so that queries can be written once with ? and run
// on either SQLite or PostgreSQL.
type dbx struct {
	*sql.DB
	pg        bool
	dsn       string
	retryConf // the stall budget (dbretry.go)
}

type txx struct {
	*sql.Tx
	pg bool
}

func rebind(pg bool, q string) string { return store.Rebind(pg, q) }

// isPostgres reports whether a database address is a PostgreSQL URL.
func isPostgres(addr string) bool {
	return strings.HasPrefix(addr, "postgres://") || strings.HasPrefix(addr, "postgresql://")
}

// openDB opens SQLite at a file path, or PostgreSQL at a postgres:// URL,
// brings the schema up to the one this build knows (see migrate), and checks
// that the recorded schema's tables and columns exist. A database that does
// not answer is tried again for up to wait.
func openDB(addr string, wait time.Duration, known int, log *slog.Logger) (*dbx, SchemaInfo, error) {
	var d *dbx
	if isPostgres(addr) {
		db, err := sql.Open("pgx", withApplicationName(addr))
		if err != nil {
			return nil, SchemaInfo{}, err
		}
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(10)
		d = &dbx{DB: db, pg: true, dsn: withApplicationName(addr)}
	} else {
		q := url.Values{}
		// busy_timeout comes first: switching to WAL needs a lock, and without
		// the timeout already set, several processes opening a new database at
		// once fail with "database is locked" instead of waiting their turn.
		for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
			q.Add("_pragma", p)
		}
		// Writers take the lock when they begin, so a transaction never
		// fails half-way because another one committed first.
		q.Set("_txlock", "immediate")
		db, err := sql.Open("sqlite", "file:"+addr+"?"+q.Encode())
		if err != nil {
			return nil, SchemaInfo{}, err
		}
		db.SetMaxOpenConns(8)
		d = &dbx{DB: db}
	}
	if err := connect(d, wait, log); err != nil {
		d.Close()
		return nil, SchemaInfo{}, fmt.Errorf("connecting to the database: %w", err)
	}
	info, err := migrate(d, known, log)
	if err != nil {
		d.Close()
		return nil, info, err
	}
	if err := checkSchemaMarkers(d, info.Database, log); err != nil {
		d.Close()
		return nil, info, err
	}
	return d, info, nil
}

// connect waits for the database to answer, trying again with growing
// pauses until wait has passed.
func connect(d *dbx, wait time.Duration, log *slog.Logger) error {
	deadline := time.Now().Add(wait)
	pause := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := d.PingContext(ctx)
		cancel()
		// SQLite reports "database is locked" at once, without waiting, when
		// another process is switching a new file to WAL at the same moment
		// (seen with several engines opening a fresh database on one CPU).
		// That clears in milliseconds, so wait for it quietly, whatever
		// `wait` says.
		for busyUntil := time.Now().Add(5 * time.Second); err != nil && !d.pg &&
			strings.Contains(err.Error(), "database is locked") && time.Now().Before(busyUntil); {
			time.Sleep(20 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = d.PingContext(ctx)
			cancel()
		}
		left := time.Until(deadline)
		if err == nil || left <= 0 {
			return err
		}
		pause = min(pause, left)
		log.Warn("the database does not answer yet; trying again", "attempt", attempt, "in", pause, "err", err)
		time.Sleep(pause)
		pause = min(2*pause, 8*time.Second)
	}
}

// render fills in the few types that differ between the two databases.
func render(pg bool, m string) string { return store.Render(pg, m) }

// compat says whether a build that knows only the migrations before one can
// keep running against a database that has it. See
// docs/development/migrations.md.
type compat int

const (
	// additive: older code keeps working. New tables, new columns that
	// are nullable or have a default, new indexes, and nothing that older
	// code would misread: no new meaning for old columns, no constraint
	// that older writes break. Event types and pipeline keys that older
	// code does not know are allowed from schema 8 on, because servers
	// from then on leave alone runs they do not fully understand (see
	// RunState.NotUnderstood).
	additive compat = iota + 1
	// breaking: code older than this migration must not run against the
	// database. Applying one raises the database's minimum code schema.
	breaking
)

func (c compat) String() string {
	if c == breaking {
		return "breaking"
	}
	return "additive"
}

// migration is one step of the schema.
type migration struct {
	compat compat
	sql    string
}

// migrations are applied in order, once each. Never edit one that has
// shipped; add a new one, and declare honestly whether code that does not
// know it can keep running (see compat).
//
// The classes of 1 to 7 were declared after they shipped. A build older
// than schema 8 refuses to start against any newer database whatever the
// class, so they only matter for the minimum stored from schema 8 on.
var migrations = []migration{
	// 1: the event log and the tables derived from it.
	//    Compatibility: breaking: the first schema: there is nothing older.
	{breaking, `
CREATE TABLE events (
  seq    {{ID}},
  run_id TEXT    NOT NULL,
  type   TEXT    NOT NULL,
  at     {{BIG}} NOT NULL,
  data   TEXT    NOT NULL
);
CREATE INDEX events_run ON events(run_id, seq);

CREATE TABLE runs (
  id               TEXT PRIMARY KEY,
  project          TEXT    NOT NULL,
  number           INTEGER NOT NULL,
  pipeline         TEXT    NOT NULL,
  status           TEXT    NOT NULL,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  event            TEXT    NOT NULL DEFAULT '',
  ref              TEXT    NOT NULL DEFAULT '',
  branch           TEXT    NOT NULL DEFAULT '',
  tag              TEXT    NOT NULL DEFAULT '',
  sha              TEXT    NOT NULL DEFAULT '',
  actor            TEXT    NOT NULL DEFAULT '',
  message          TEXT    NOT NULL DEFAULT '',
  author           TEXT    NOT NULL DEFAULT '',
  created_at       {{BIG}} NOT NULL,
  started_at       {{BIG}} NOT NULL DEFAULT 0,
  finished_at      {{BIG}} NOT NULL DEFAULT 0,
  last_seq         {{BIG}} NOT NULL,
  UNIQUE(project, number)
);
CREATE INDEX runs_created ON runs(created_at DESC);
CREATE INDEX runs_project ON runs(project, number DESC);

CREATE TABLE steps (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  position    INTEGER NOT NULL,
  status      TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  queued_at   {{BIG}} NOT NULL DEFAULT 0,
  started_at  {{BIG}} NOT NULL DEFAULT 0,
  finished_at {{BIG}} NOT NULL DEFAULT 0,
  exit_code   INTEGER,
  error       TEXT    NOT NULL DEFAULT '',
  skip_kind   TEXT    NOT NULL DEFAULT '',
  runner      TEXT    NOT NULL DEFAULT '',
  lease_id    TEXT    NOT NULL DEFAULT '',
  outputs     TEXT    NOT NULL DEFAULT '{}',
  PRIMARY KEY (run_id, step_id)
);
CREATE INDEX steps_status ON steps(status, queued_at);

CREATE TABLE logs (
  run_id  TEXT    NOT NULL,
  step_id TEXT    NOT NULL,
  attempt INTEGER NOT NULL,
  seq     INTEGER NOT NULL,
  at      {{BIG}} NOT NULL,
  stream  TEXT    NOT NULL,
  text    TEXT    NOT NULL,
  PRIMARY KEY (run_id, step_id, attempt, seq)
);

CREATE TABLE runners (
  id        TEXT PRIMARY KEY,
  name      TEXT    NOT NULL,
  executor  TEXT    NOT NULL DEFAULT '',
  version   TEXT    NOT NULL DEFAULT '',
  last_seen {{BIG}} NOT NULL
);

CREATE TABLE refs_seen (
  project TEXT NOT NULL,
  ref     TEXT NOT NULL,
  sha     TEXT NOT NULL,
  PRIMARY KEY (project, ref)
);
`},
	// 2: several servers sharing one database.
	//    run_seq orders each run's events, so two servers changing the same
	//    run at once cannot both succeed; leases and push decisions
	//    move out of memory so any server can serve any request.
	//    refs_seen.seen_at is when the server that recorded a ref's commit
	//    had fetched the repository, so a server whose mirror is older cannot
	//    move the ref back.
	//    Compatibility: breaking: schema-1 code inserts events without run_seq, so the second
	//    event of any run breaks the unique index, and it keeps leases in memory.
	{breaking, `
ALTER TABLE events ADD COLUMN run_seq {{BIG}} NOT NULL DEFAULT 0;
UPDATE events SET run_seq = (SELECT COUNT(*) FROM events e2 WHERE e2.run_id = events.run_id AND e2.seq <= events.seq);
CREATE UNIQUE INDEX events_run_seq ON events(run_id, run_seq);

CREATE TABLE leases (
  id         TEXT PRIMARY KEY,
  run_id     TEXT    NOT NULL,
  step_id    TEXT    NOT NULL,
  attempt    INTEGER NOT NULL,
  runner     TEXT    NOT NULL,
  expires_at {{BIG}} NOT NULL,
  log_seq    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX leases_expires ON leases(expires_at);
CREATE INDEX leases_step ON leases(run_id, step_id);
INSERT INTO leases(id, run_id, step_id, attempt, runner, expires_at, log_seq)
  SELECT lease_id, run_id, step_id, attempt, runner, 0,
         COALESCE((SELECT MAX(l.seq) FROM logs l WHERE l.run_id = steps.run_id AND l.step_id = steps.step_id AND l.attempt = steps.attempt), 0)
  FROM steps WHERE status IN ('leased', 'running') AND lease_id <> '';

ALTER TABLE refs_seen ADD COLUMN seen_at {{BIG}} NOT NULL DEFAULT 0;

CREATE TABLE push_decisions (
  id      {{ID}},
  project TEXT    NOT NULL,
  at      {{BIG}} NOT NULL,
  ref     TEXT    NOT NULL,
  sha     TEXT    NOT NULL,
  outcome TEXT    NOT NULL,
  detail  TEXT    NOT NULL,
  run_id  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX push_decisions_project ON push_decisions(project, at DESC);
`},
	// 3: a counter per project for run numbers, so that creating many runs
	//    at once never collides.
	//    Compatibility: breaking: schema-2 code numbered runs with MAX(number) + 1 without
	//    moving the counter, so newer code would then hand out numbers already
	//    taken and fail to create runs for that project.
	{breaking, `
CREATE TABLE run_counters (
  project TEXT PRIMARY KEY,
  last    INTEGER NOT NULL
);
INSERT INTO run_counters(project, last) SELECT project, MAX(number) FROM runs GROUP BY project;
`},
	// 4: approvals. A waiting approval has a deadline, found by every
	//    server's reaper; a decided one records who decided, when and why.
	//    Compatibility: breaking: the columns are additive, but schema-3 code did not know
	//    approval steps or their events and would have run straight past them.
	{breaking, `
ALTER TABLE steps ADD COLUMN deadline {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN decided_by TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN decided_at {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN comment TEXT NOT NULL DEFAULT '';
`},
	// 5: sign-in. Sessions and API tokens are kept here so that any server
	//    accepts them; only SHA-256 hashes of their secret values are
	//    stored. users holds the groups from each person's latest sign-in,
	//    which API tokens act with. audit is only ever appended to.
	//    Compatibility: additive: new tables only; older code ignores them.
	{additive, `
CREATE TABLE users (
  username   TEXT PRIMARY KEY,
  group_list TEXT    NOT NULL,
  last_login {{BIG}} NOT NULL
);

CREATE TABLE sessions (
  id_hash    TEXT PRIMARY KEY,
  username   TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  last_seen  {{BIG}} NOT NULL,
  expires_at {{BIG}} NOT NULL
);
CREATE INDEX sessions_expires ON sessions(expires_at);

CREATE TABLE api_tokens (
  id         TEXT PRIMARY KEY,
  hash       TEXT    NOT NULL UNIQUE,
  username   TEXT    NOT NULL,
  name       TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  expires_at {{BIG}} NOT NULL,
  last_used  {{BIG}} NOT NULL DEFAULT 0,
  UNIQUE(username, name)
);

CREATE TABLE audit (
  id       {{ID}},
  at       {{BIG}} NOT NULL,
  username TEXT    NOT NULL,
  source   TEXT    NOT NULL,
  action   TEXT    NOT NULL,
  project  TEXT    NOT NULL,
  target   TEXT    NOT NULL,
  outcome  TEXT    NOT NULL,
  detail   TEXT    NOT NULL
);
CREATE INDEX audit_at ON audit(at);
CREATE INDEX audit_username ON audit(username, id);
CREATE INDEX audit_project ON audit(project, id);
`},
	// 6: run affinity. runs.runner is the runner holding the run's
	//    workspace, so that a run's steps go to it (see Engine.Lease). Runs in
	//    flight during the upgrade start with none, and go to any runner.
	//    Compatibility: additive: a new column with a default; older code leases without
	//    affinity, as it always did.
	{additive, `
ALTER TABLE runs ADD COLUMN runner TEXT NOT NULL DEFAULT '';
CREATE INDEX leases_runner ON leases(runner);
`},
	// 7: the pipeline editor. A draft run is marked, with the hash of the
	//    text it ran (the text itself is in its run.created event); a failed
	//    step keeps the last lines of its output; and people's display
	//    names and e-mail addresses, as their provider last gave them, are
	//    kept to author the commits they save.
	//    Compatibility: breaking: the columns are additive, but schema-6 code did not know
	//    draft runs: it would run their steps as ordinary ones, with secrets and
	//    with conditions that keep deployments out of drafts ignored.
	{breaking, `
ALTER TABLE runs ADD COLUMN draft INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN pipeline_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN excerpt TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
`},
	// 8: what each step attempt was given, re-runs, and the schema each
	//    run needs. step_inputs is derived from the inputs in step.leased
	//    events, one row per value, so that it can be searched; a secret's
	//    row holds its reference, never a value. runs.min_schema is the
	//    oldest schema whose servers understand every event of the run (see
	//    RunState.Schema): servers that know less leave the run alone. A
	//    re-run names the run it re-runs and the step it starts from; a step
	//    it reused names the run where that step actually ran.
	//    Compatibility: additive: new columns with defaults and a new table.
	//    The re-run's step.reused events are not understood by older servers,
	//    which from this schema on leave such runs alone.
	{additive, `
ALTER TABLE runs ADD COLUMN min_schema INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN rerun_of TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN rerun_of_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN rerun_from TEXT NOT NULL DEFAULT '';
CREATE INDEX runs_rerun_of ON runs(rerun_of);
ALTER TABLE steps ADD COLUMN reused_from TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN reused_from_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN reused_attempt INTEGER NOT NULL DEFAULT 0;

CREATE TABLE step_inputs (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  kind        TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  value       TEXT    NOT NULL,
  origin      TEXT    NOT NULL DEFAULT '',
  origin_step TEXT    NOT NULL DEFAULT '',
  origin_key  TEXT    NOT NULL DEFAULT '',
  truncated   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, step_id, attempt, kind, name)
);
`},
	// 9: pull requests and superseding. Runs started by a pull request
	//    (trigger event pull_request) need a server that knows them: their
	//    steps read no secrets unless the project allows it, which older
	//    servers would not enforce. Their pipeline always has
	//    on.pull_request, which older servers do not know, so they leave
	//    such runs alone, as they do runs whose pipeline uses supersede;
	//    runs.min_schema says 9 for both. A run cancelled
	//    because a newer run of the same ref superseded it records why, and
	//    which run: cancel_reason and superseded_by are derived from the
	//    run.cancel_requested event, whose new fields older servers ignore
	//    (a cancel is a cancel to them). The index finds the runs of a ref
	//    to supersede.
	//    Compatibility: additive: new columns with defaults and an index;
	//    no new event type. Older servers never write the new columns: if
	//    one resumes a superseded run and it is then cancelled by hand, the
	//    old reason stays in the row (the events still tell the truth).
	{additive, `
ALTER TABLE runs ADD COLUMN cancel_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN superseded_by TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN superseded_by_number INTEGER NOT NULL DEFAULT 0;
CREATE INDEX runs_project_ref ON runs(project, ref);
`},
	// 10: test reports. A step may declare test result files (reports.tests
	//    in its pipeline), which its runner collects when it ends and the
	//    server parses. The step.finished or step.canceled event of the
	//    attempt carries a short summary (Payload.Tests): the events stay the
	//    truth, and stay small. test_reports is derived from those events,
	//    one row per step attempt. test_cases holds the individual results,
	//    written in the same transaction as the event; like logs, they are
	//    kept beside the events rather than in them, and are pruned oldest
	//    run first when a project has more than the configured number.
	//    Compatibility: additive: new tables only, and no new event type.
	//    Older servers ignore the tables and the summary field (it only
	//    records). Runs whose pipeline uses reports have min_schema 10: older
	//    servers do not know the key, so they leave such runs alone, as they
	//    would have to anyway (their strict reading of the pipeline fails).
	{additive, `
CREATE TABLE test_reports (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  at          {{BIG}} NOT NULL,
  total       INTEGER NOT NULL DEFAULT 0,
  passed      INTEGER NOT NULL DEFAULT 0,
  failed      INTEGER NOT NULL DEFAULT 0,
  errored     INTEGER NOT NULL DEFAULT 0,
  skipped     INTEGER NOT NULL DEFAULT 0,
  flaky       INTEGER NOT NULL DEFAULT 0,
  duration_ms {{BIG}} NOT NULL DEFAULT 0,
  truncated   INTEGER NOT NULL DEFAULT 0,
  stored      INTEGER NOT NULL DEFAULT 0,
  pruned      INTEGER NOT NULL DEFAULT 0,
  detail      TEXT    NOT NULL DEFAULT '{}',
  PRIMARY KEY (run_id, step_id, attempt)
);
CREATE INDEX test_reports_project ON test_reports(project, run_number);

CREATE TABLE test_cases (
  id          {{ID}},
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  test_key    TEXT    NOT NULL,
  suite       TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  file        TEXT    NOT NULL DEFAULT '',
  status      TEXT    NOT NULL,
  duration_ms {{BIG}} NOT NULL DEFAULT 0,
  message     TEXT    NOT NULL DEFAULT '',
  details     TEXT    NOT NULL DEFAULT '',
  output      TEXT    NOT NULL DEFAULT '',
  flaky       INTEGER NOT NULL DEFAULT 0,
  report_file TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX test_cases_run ON test_cases(run_id, step_id, attempt);
CREATE INDEX test_cases_history ON test_cases(project, test_key, run_number);
CREATE INDEX test_cases_prune ON test_cases(project, run_number);
`},
	// 11: deployments. Deploy and rollback steps (kinds deploy and rollback)
	//    and their deploy.* events. steps.target is the target a deploy
	//    step's actions go to (only deploy runners serving it lease them),
	//    and steps.wait_kind says what a waiting step waits for: '' for an
	//    approval, lock or observe for a deploy step. runners.targets and
	//    runners.pool are what a deploy runner serves. deployments,
	//    environment_state, deploy_locks and deploy_queue are derived from
	//    the events, in the transaction that stores them; the snapshot
	//    columns of deployments and deployment_timeline hold what deploy
	//    runners observe, which is not in the events (it changes too often).
	//    target_observers picks one runner to observe each target.
	//    deploy_commands is for a later release's commands (promote, abort);
	//    nothing writes it yet.
	//    Compatibility: additive: new tables, and new columns with defaults.
	//    Runs with deploy steps have min_schema 11 from creation: older
	//    servers do not know the step kinds (nor the pipeline keys), so they
	//    leave such runs alone, their locks included.
	{additive, `
ALTER TABLE steps ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN wait_kind TEXT NOT NULL DEFAULT '';
CREATE INDEX steps_target ON steps(target, status, queued_at);
ALTER TABLE runners ADD COLUMN targets TEXT NOT NULL DEFAULT '';
ALTER TABLE runners ADD COLUMN pool TEXT NOT NULL DEFAULT '';

CREATE TABLE deployments (
  id               TEXT PRIMARY KEY,
  run_id           TEXT    NOT NULL,
  run_number       INTEGER NOT NULL,
  project          TEXT    NOT NULL,
  step_id          TEXT    NOT NULL,
  environment      TEXT    NOT NULL,
  service          TEXT    NOT NULL,
  target           TEXT    NOT NULL,
  application      TEXT    NOT NULL,
  mode             TEXT    NOT NULL,
  version          TEXT    NOT NULL,
  previous_id      TEXT    NOT NULL DEFAULT '',
  previous_version TEXT    NOT NULL DEFAULT '',
  repo             TEXT    NOT NULL DEFAULT '',
  branch           TEXT    NOT NULL DEFAULT '',
  commit_sha       TEXT    NOT NULL DEFAULT '',
  changed          INTEGER NOT NULL DEFAULT 0,
  revert_sha       TEXT    NOT NULL DEFAULT '',
  rollback_step    TEXT    NOT NULL DEFAULT '',
  phase            TEXT    NOT NULL DEFAULT '',
  detail           TEXT    NOT NULL DEFAULT '',
  verdict          TEXT    NOT NULL DEFAULT '',
  opened_at        {{BIG}} NOT NULL,
  deployed_at      {{BIG}} NOT NULL DEFAULT 0,
  closed_at        {{BIG}} NOT NULL DEFAULT 0,
  snapshot         TEXT    NOT NULL DEFAULT '',
  snapshot_key     TEXT    NOT NULL DEFAULT '',
  observed_at      {{BIG}} NOT NULL DEFAULT 0,
  observed_by      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX deployments_run ON deployments(run_id);
CREATE INDEX deployments_unit ON deployments(environment, project, service, opened_at);

CREATE TABLE environment_state (
  environment        TEXT    NOT NULL,
  project            TEXT    NOT NULL,
  service            TEXT    NOT NULL,
  current_deployment TEXT    NOT NULL DEFAULT '',
  current_version    TEXT    NOT NULL DEFAULT '',
  current_run        TEXT    NOT NULL DEFAULT '',
  current_run_number INTEGER NOT NULL DEFAULT 0,
  since              {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (environment, project, service)
);

CREATE TABLE deploy_locks (
  environment TEXT    NOT NULL,
  project     TEXT    NOT NULL,
  service     TEXT    NOT NULL,
  deployment  TEXT    NOT NULL,
  run_id      TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  step_id     TEXT    NOT NULL,
  since       {{BIG}} NOT NULL,
  PRIMARY KEY (environment, project, service)
);
CREATE INDEX deploy_locks_deployment ON deploy_locks(deployment);

CREATE TABLE deploy_queue (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  environment TEXT    NOT NULL,
  project     TEXT    NOT NULL,
  service     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  since       {{BIG}} NOT NULL,
  PRIMARY KEY (run_id, step_id)
);
CREATE INDEX deploy_queue_unit ON deploy_queue(environment, project, service, run_number);

CREATE TABLE deployment_timeline (
  id         {{ID}},
  deployment TEXT    NOT NULL,
  step_id    TEXT    NOT NULL,
  at         {{BIG}} NOT NULL,
  phase      TEXT    NOT NULL,
  detail     TEXT    NOT NULL
);
CREATE INDEX deployment_timeline_dep ON deployment_timeline(deployment, id);

CREATE TABLE deploy_commands (
  id         TEXT PRIMARY KEY,
  deployment TEXT    NOT NULL,
  run_id     TEXT    NOT NULL,
  step_id    TEXT    NOT NULL,
  action     TEXT    NOT NULL,
  actor      TEXT    NOT NULL,
  comment    TEXT    NOT NULL DEFAULT '',
  expect     TEXT    NOT NULL DEFAULT '{}',
  state      TEXT    NOT NULL DEFAULT '',
  result     TEXT    NOT NULL DEFAULT '',
  created_at {{BIG}} NOT NULL,
  done_at    {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX deploy_commands_dep ON deploy_commands(deployment, created_at);

CREATE TABLE target_observers (
  target     TEXT PRIMARY KEY,
  runner     TEXT    NOT NULL,
  since      {{BIG}} NOT NULL,
  expires_at {{BIG}} NOT NULL
);
`},
	// 12: artifacts, security findings and SBOMs (see artifacts.go,
	//    findings.go). Artifacts are files a step keeps: their content is in
	//    a blob store (a directory, or S3-compatible object storage when
	//    several servers run), described here. artifacts has one row per
	//    artifact of a step attempt; artifact_files its files; artifact_blobs
	//    the stored contents, found again by SHA-256 so that a file kept twice
	//    is stored once, with the state of uploads in progress so that any
	//    server can take the next chunk; artifact_usage the bytes each project
	//    keeps, for its quota. Findings and SBOM components are kept like
	//    test results: a summary in the step.finished or step.canceled event
	//    (Payload.Findings, Payload.SBOM, Payload.Artifacts) and derived
	//    tables beside the events, pruned oldest run first per project.
	//    Compatibility: additive: new tables only, and no new event type.
	//    Older servers ignore the tables and the summary fields (they only
	//    record). Runs whose pipeline uses artifacts, fetch, reports.findings
	//    or reports.sbom have min_schema 12, so older servers leave them
	//    alone, as they would have to: they cannot read those keys.
	{additive, `
CREATE TABLE artifacts (
  id          TEXT    PRIMARY KEY,
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  name        TEXT    NOT NULL,
  kind        TEXT    NOT NULL DEFAULT '',
  status      TEXT    NOT NULL,
  files       INTEGER NOT NULL DEFAULT 0,
  bytes       {{BIG}} NOT NULL DEFAULT 0,
  retain_ms   {{BIG}} NOT NULL DEFAULT 0,
  created_at  {{BIG}} NOT NULL,
  kept_at     {{BIG}} NOT NULL DEFAULT 0,
  expires_at  {{BIG}} NOT NULL DEFAULT 0,
  ended_at    {{BIG}} NOT NULL DEFAULT 0,
  ended_by    TEXT    NOT NULL DEFAULT '',
  note        TEXT    NOT NULL DEFAULT '',
  UNIQUE (run_id, step_id, attempt, name)
);
CREATE INDEX artifacts_status ON artifacts(status, expires_at);
CREATE INDEX artifacts_project ON artifacts(project, run_number);

CREATE TABLE artifact_files (
  artifact_id TEXT    NOT NULL,
  path        TEXT    NOT NULL,
  size        {{BIG}} NOT NULL,
  sha256      TEXT    NOT NULL,
  mode        INTEGER NOT NULL DEFAULT 420,
  blob_id     TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (artifact_id, path)
);
CREATE INDEX artifact_files_blob ON artifact_files(blob_id);

CREATE TABLE artifact_blobs (
  id          TEXT    PRIMARY KEY,
  sha256      TEXT    NOT NULL,
  size        {{BIG}} NOT NULL,
  store_key   TEXT    NOT NULL,
  state       TEXT    NOT NULL,
  upload_id   TEXT    NOT NULL DEFAULT '',
  received    {{BIG}} NOT NULL DEFAULT 0,
  hash_state  TEXT    NOT NULL DEFAULT '',
  parts       TEXT    NOT NULL DEFAULT '[]',
  created_at  {{BIG}} NOT NULL,
  used_at     {{BIG}} NOT NULL
);
CREATE INDEX artifact_blobs_sha ON artifact_blobs(sha256, size);
CREATE INDEX artifact_blobs_state ON artifact_blobs(state, used_at);

CREATE TABLE artifact_usage (
  project     TEXT    PRIMARY KEY,
  bytes       {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE finding_reports (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  at          {{BIG}} NOT NULL,
  total       INTEGER NOT NULL DEFAULT 0,
  critical    INTEGER NOT NULL DEFAULT 0,
  high        INTEGER NOT NULL DEFAULT 0,
  medium      INTEGER NOT NULL DEFAULT 0,
  low         INTEGER NOT NULL DEFAULT 0,
  unknown     INTEGER NOT NULL DEFAULT 0,
  suppressed  INTEGER NOT NULL DEFAULT 0,
  truncated   INTEGER NOT NULL DEFAULT 0,
  stored      INTEGER NOT NULL DEFAULT 0,
  pruned      INTEGER NOT NULL DEFAULT 0,
  tools       TEXT    NOT NULL DEFAULT '[]',
  detail      TEXT    NOT NULL DEFAULT '{}',
  PRIMARY KEY (run_id, step_id, attempt)
);
CREATE INDEX finding_reports_project ON finding_reports(project, run_number);

CREATE TABLE findings (
  id           {{ID}},
  run_id       TEXT    NOT NULL,
  step_id      TEXT    NOT NULL,
  attempt      INTEGER NOT NULL,
  project      TEXT    NOT NULL,
  run_number   INTEGER NOT NULL,
  fingerprint  TEXT    NOT NULL,
  tool         TEXT    NOT NULL,
  rule_id      TEXT    NOT NULL,
  severity     TEXT    NOT NULL,
  level        TEXT    NOT NULL DEFAULT '',
  message      TEXT    NOT NULL DEFAULT '',
  file         TEXT    NOT NULL DEFAULT '',
  start_line   INTEGER NOT NULL DEFAULT 0,
  end_line     INTEGER NOT NULL DEFAULT 0,
  start_column INTEGER NOT NULL DEFAULT 0,
  secret       INTEGER NOT NULL DEFAULT 0,
  report_file  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX findings_run ON findings(run_id, step_id, attempt);
CREATE INDEX findings_history ON findings(project, fingerprint, run_number);
CREATE INDEX findings_prune ON findings(project, run_number);

CREATE TABLE finding_rules (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  tool        TEXT    NOT NULL,
  rule_id     TEXT    NOT NULL,
  name        TEXT    NOT NULL DEFAULT '',
  short       TEXT    NOT NULL DEFAULT '',
  full_text   TEXT    NOT NULL DEFAULT '',
  help        TEXT    NOT NULL DEFAULT '',
  help_uri    TEXT    NOT NULL DEFAULT '',
  tags        TEXT    NOT NULL DEFAULT '[]',
  PRIMARY KEY (run_id, step_id, attempt, tool, rule_id)
);
CREATE INDEX finding_rules_prune ON finding_rules(project, run_number);

CREATE TABLE sbom_reports (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  at          {{BIG}} NOT NULL,
  components  INTEGER NOT NULL DEFAULT 0,
  stored      INTEGER NOT NULL DEFAULT 0,
  truncated   INTEGER NOT NULL DEFAULT 0,
  pruned      INTEGER NOT NULL DEFAULT 0,
  licences    TEXT    NOT NULL DEFAULT '{}',
  detail      TEXT    NOT NULL DEFAULT '{}',
  PRIMARY KEY (run_id, step_id, attempt)
);
CREATE INDEX sbom_reports_project ON sbom_reports(project, run_number);

CREATE TABLE sbom_components (
  id          {{ID}},
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  attempt     INTEGER NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  name        TEXT    NOT NULL,
  name_key    TEXT    NOT NULL,
  version     TEXT    NOT NULL DEFAULT '',
  type        TEXT    NOT NULL DEFAULT '',
  purl        TEXT    NOT NULL DEFAULT '',
  licences    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sbom_components_run ON sbom_components(run_id, step_id, attempt);
CREATE INDEX sbom_components_name ON sbom_components(name_key, version);
CREATE INDEX sbom_components_prune ON sbom_components(project, run_number);
`},
	// 13: run inputs, loops, schedules, webhooks, and API tokens that are
	//    limited. A run's input values are in its run.created event (in the
	//    trigger); its pipeline then declares inputs, so runs.min_schema is
	//    13 and older servers leave it alone, as they do runs with loops,
	//    and runs started by a schedule or a webhook.
	//    schedule_seen records when each schedule (by its key, a hash of what
	//    it says) was first seen, so it never fires for times before that.
	//    schedule_fires claims each firing: the primary key lets exactly one
	//    server act on it, as refs_seen does for pushes, and the row says
	//    what came of it. webhook_deliveries remembers the deliveries of Git
	//    host webhooks (to refuse replays) and the idempotency keys of
	//    webhook triggers (to answer a repeat with the run the first call
	//    started).
	//    api_tokens gains the role and projects a token is limited to, and
	//    where it was last used from. A limited token's hash is stored with
	//    a "v2:" prefix, which servers older than this never look up: they
	//    refuse the token rather than grant it its owner's full rights.
	//    Compatibility: additive: new tables, and new columns with defaults
	//    that older servers do not need. No new event type.
	{additive, `
CREATE TABLE schedule_seen (
  project    TEXT    NOT NULL,
  sched_key  TEXT    NOT NULL,
  first_seen {{BIG}} NOT NULL,
  PRIMARY KEY (project, sched_key)
);

CREATE TABLE schedule_fires (
  project   TEXT    NOT NULL,
  sched_key TEXT    NOT NULL,
  fire_at   {{BIG}} NOT NULL,
  at        {{BIG}} NOT NULL,
  outcome   TEXT    NOT NULL,
  detail    TEXT    NOT NULL DEFAULT '',
  run_id    TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (project, sched_key, fire_at)
);
CREATE INDEX schedule_fires_project ON schedule_fires(project, at);

CREATE TABLE webhook_deliveries (
  project   TEXT    NOT NULL,
  hook      TEXT    NOT NULL,
  delivery  TEXT    NOT NULL,
  at        {{BIG}} NOT NULL,
  body_hash TEXT    NOT NULL DEFAULT '',
  run_id    TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (project, hook, delivery)
);
CREATE INDEX webhook_deliveries_at ON webhook_deliveries(at);

ALTER TABLE api_tokens ADD COLUMN role TEXT NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN projects TEXT NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN last_used_from TEXT NOT NULL DEFAULT '';
`},
	// 14: controlling deployments (see control.go). Commands on a
	//    deployment's Argo Rollouts rollout (promote, promote-full, abort,
	//    retry) are deploy.command* events; deploy_commands (made empty by
	//    11) gets the columns deploy runners take them by. Rollout steps
	//    (kind rollout) and the keys of control.go in internal/pipeline give
	//    a run min_schema 14, so older servers leave it alone, and a command
	//    given on an older run raises its min_schema the same way.
	//    deployments.pod_hash is the deployment's pod template hash, the
	//    first time a deploy runner saw it in the rollout, and watch_spec
	//    what to watch it for once its run is over. environment_state gets
	//    what deploy runners last saw of an idle service, and its drift.
	//    Compatibility: additive: new columns with defaults and an index.
	{additive, `
ALTER TABLE deploy_commands ADD COLUMN project TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_commands ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_commands ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_commands ADD COLUMN take_id TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_commands ADD COLUMN taken_by TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_commands ADD COLUMN taken_until {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE deploy_commands ADD COLUMN tries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deploy_commands ADD COLUMN detail TEXT NOT NULL DEFAULT '';
ALTER TABLE deploy_commands ADD COLUMN withdrawn_by TEXT NOT NULL DEFAULT '';
CREATE INDEX deploy_commands_open ON deploy_commands(state, target);
ALTER TABLE deployments ADD COLUMN pod_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN watch_spec TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_state ADD COLUMN snapshot TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_state ADD COLUMN observed_at {{BIG}} NOT NULL DEFAULT 0;
ALTER TABLE environment_state ADD COLUMN observed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_state ADD COLUMN observed_deployment TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_state ADD COLUMN drift TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_state ADD COLUMN drift_detail TEXT NOT NULL DEFAULT '';
`},
	// 15: image steps (kind image; see images.go). What an image step
	//    built is a summary in the event that ends its attempt
	//    (Payload.Image), and derived from it: images, one row per attempt
	//    with the summary, and image_refs, one row per name it was pushed
	//    under and per digest (the index's and each platform's), for "which
	//    run built this digest" and "the latest image of this repository".
	//    The SBOM, the findings and the provenance are the step's reports and
	//    artifacts, kept as schema 12 keeps any step's. Deploy changes may
	//    pin an image by digest. runners.features keeps what each runner
	//    said it can do, so a run can say it waits for one that builds images.
	//    Compatibility: additive: new tables only, and no new event type.
	//    A run whose pipeline has an image step or a digest change has
	//    min_schema 15 from creation (the pipeline also carries "requires":
	//    15), so older servers leave it alone: they would not know the step
	//    kind, and a deploy runner older than this refuses a change with a
	//    digest and no tag rather than writing a tag.
	{additive, `
CREATE TABLE images (
  run_id     TEXT    NOT NULL,
  step_id    TEXT    NOT NULL,
  attempt    INTEGER NOT NULL,
  project    TEXT    NOT NULL,
  run_number INTEGER NOT NULL,
  digest     TEXT    NOT NULL,
  image      TEXT    NOT NULL DEFAULT '',
  pushed     INTEGER NOT NULL DEFAULT 0,
  signed     INTEGER NOT NULL DEFAULT 0,
  outcome    TEXT    NOT NULL DEFAULT '',
  summary    TEXT    NOT NULL,
  created_at {{BIG}} NOT NULL,
  PRIMARY KEY (run_id, step_id, attempt)
);
CREATE INDEX images_project ON images(project, created_at);

CREATE TABLE image_refs (
  run_id     TEXT    NOT NULL,
  step_id    TEXT    NOT NULL,
  attempt    INTEGER NOT NULL,
  project    TEXT    NOT NULL,
  run_number INTEGER NOT NULL,
  kind       TEXT    NOT NULL,
  repository TEXT    NOT NULL DEFAULT '',
  tag        TEXT    NOT NULL DEFAULT '',
  digest     TEXT    NOT NULL,
  platform   TEXT    NOT NULL DEFAULT '',
  pushed     INTEGER NOT NULL DEFAULT 0,
  created_at {{BIG}} NOT NULL
);
CREATE INDEX image_refs_digest ON image_refs(digest, created_at);
CREATE INDEX image_refs_repository ON image_refs(repository, created_at);
CREATE INDEX image_refs_run ON image_refs(run_id, step_id, attempt);

ALTER TABLE runners ADD COLUMN features TEXT NOT NULL DEFAULT '';
`},

	// 16: delivery policies (see policy.go and internal/policy). The
	//     policy.evaluated event records each decision with the policies,
	//     the facts and the waivers it was judged from; policy_evaluations
	//     is derived from those events. policy_sources holds what servers
	//     loaded from Git sources of policies, so every server judges from
	//     the same policies; policy_waiver_events is the log of waivers
	//     granted and revoked, and policy_waivers is derived from it.
	//     Compatibility: additive. New tables only, which older servers
	//     never read. A run judged by a policy has a policy.evaluated event
	//     from its creation (or from the first gate it meets), which gives it
	//     min_schema 16, so older servers leave it alone rather than run its
	//     steps without the gates. Findings summaries gain by_tool, which
	//     older servers ignore, as it only records.
	{additive, `
CREATE TABLE policy_sources (
  name           TEXT PRIMARY KEY,
  ref            TEXT    NOT NULL DEFAULT '',
  version        TEXT    NOT NULL DEFAULT '',
  policies       TEXT    NOT NULL DEFAULT '',
  error          TEXT    NOT NULL DEFAULT '',
  loaded_at      {{BIG}} NOT NULL DEFAULT 0,
  checked_at     {{BIG}} NOT NULL DEFAULT 0,
  fetch_error    TEXT    NOT NULL DEFAULT '',
  fetch_error_at {{BIG}} NOT NULL DEFAULT 0,
  server         TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE policy_evaluations (
  run_id      TEXT    NOT NULL,
  seq         {{BIG}} NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  point       TEXT    NOT NULL,
  step_id     TEXT    NOT NULL DEFAULT '',
  environment TEXT    NOT NULL DEFAULT '',
  policy      TEXT    NOT NULL,
  source      TEXT    NOT NULL DEFAULT '',
  version     TEXT    NOT NULL DEFAULT '',
  mode        TEXT    NOT NULL DEFAULT '',
  verdict     TEXT    NOT NULL,
  at          {{BIG}} NOT NULL,
  PRIMARY KEY (run_id, seq, policy)
);
CREATE INDEX policy_evaluations_policy ON policy_evaluations(policy, at);
CREATE INDEX policy_evaluations_project ON policy_evaluations(project, at);

CREATE TABLE policy_waiver_events (
  seq       {{ID}},
  waiver_id TEXT    NOT NULL,
  type      TEXT    NOT NULL,
  at        {{BIG}} NOT NULL,
  actor     TEXT    NOT NULL,
  data      TEXT    NOT NULL
);
CREATE INDEX policy_waiver_events_waiver ON policy_waiver_events(waiver_id, seq);

CREATE TABLE policy_waivers (
  id            TEXT PRIMARY KEY,
  project       TEXT    NOT NULL,
  policy        TEXT    NOT NULL,
  rule          TEXT    NOT NULL,
  run_id        TEXT    NOT NULL DEFAULT '',
  environment   TEXT    NOT NULL DEFAULT '',
  finding       TEXT    NOT NULL DEFAULT '',
  reason        TEXT    NOT NULL,
  granted_by    TEXT    NOT NULL,
  granted_at    {{BIG}} NOT NULL,
  expires_at    {{BIG}} NOT NULL,
  revoked_by    TEXT    NOT NULL DEFAULT '',
  revoked_at    {{BIG}} NOT NULL DEFAULT 0,
  revoke_reason TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX policy_waivers_project ON policy_waivers(project, expires_at);
`},

	// 17: the built-in blob store. Artifacts (and the SBOMs and provenance
	//     kept as artifacts) can be kept in the database itself, in chunks,
	//     so that several servers sharing a PostgreSQL database share them
	//     with nothing else to run (see blobstore.Database and
	//     docs/artifacts.md). The four tables are blobstore.DatabaseSchema,
	//     word for word. artifact_blobs.store records which kind of store
	//     each blob was written to (database, filesystem or s3; '' for blobs
	//     written before this schema), so that a server whose configured
	//     store lacks a blob can say where it is, and so that
	//     `launchway blobs copy` can move blobs between stores and resume.
	//     Compatibility: additive: new tables, and a new column with a
	//     default. Older servers ignore both. They do not know the database
	//     store: with no store configured they use the filesystem, whose
	//     marker check then turns their artifacts off (the marker is in the
	//     database store), and the pipeline of a run that keeps or fetches
	//     artifacts, created by a server using the database store, carries
	//     "requires": 17 (min_schema 17), so older servers leave it alone.
	{additive, blobstore.DatabaseSchema + `
ALTER TABLE artifact_blobs ADD COLUMN store TEXT NOT NULL DEFAULT '';
`},

	// 18: richer test reports, coverage and HTML reports (see
	//    reports18.go). test_cases gets the tags and the variant of a
	//    result (a Playwright project, a browser), for filtering, and extra:
	//    its steps, attachments, retries and properties as bounded JSON.
	//    Like test_cases, these are kept beside the events, written in the
	//    transaction that records the attempt's end. coverage_reports is
	//    derived from the coverage summary in the event that ends an attempt
	//    (Payload.Coverage), one row per attempt, and coverage_files holds
	//    each file's counts beside it, pruned with the run's test results.
	//    Test attachments and HTML reports are artifacts (schema 12) of
	//    kinds test-attachments and html-report.
	//    Compatibility: additive: new columns with defaults and new tables,
	//    no new event type. Older servers write test_cases without the new
	//    columns (they take their defaults) and ignore the new tables and
	//    the new summary fields. A run whose pipeline uses test attachments,
	//    coverage or HTML reports has min_schema 18 from creation, and older
	//    servers cannot read such a pipeline anyway (their strict reading
	//    fails on the new keys), so they leave the run alone.
	{additive, `
ALTER TABLE test_cases ADD COLUMN tags TEXT NOT NULL DEFAULT '';
ALTER TABLE test_cases ADD COLUMN variant TEXT NOT NULL DEFAULT '';
ALTER TABLE test_cases ADD COLUMN extra TEXT NOT NULL DEFAULT '';

CREATE TABLE coverage_reports (
  run_id            TEXT    NOT NULL,
  step_id           TEXT    NOT NULL,
  attempt           INTEGER NOT NULL,
  project           TEXT    NOT NULL,
  run_number        INTEGER NOT NULL,
  at                {{BIG}} NOT NULL,
  lines_covered     {{BIG}} NOT NULL DEFAULT 0,
  lines_total       {{BIG}} NOT NULL DEFAULT 0,
  branches_covered  {{BIG}} NOT NULL DEFAULT 0,
  branches_total    {{BIG}} NOT NULL DEFAULT 0,
  functions_covered {{BIG}} NOT NULL DEFAULT 0,
  functions_total   {{BIG}} NOT NULL DEFAULT 0,
  files             INTEGER NOT NULL DEFAULT 0,
  stored            INTEGER NOT NULL DEFAULT 0,
  pruned            INTEGER NOT NULL DEFAULT 0,
  detail            TEXT    NOT NULL DEFAULT '{}',
  PRIMARY KEY (run_id, step_id, attempt)
);
CREATE INDEX coverage_reports_project ON coverage_reports(project, run_number);

CREATE TABLE coverage_files (
  id                {{ID}},
  run_id            TEXT    NOT NULL,
  step_id           TEXT    NOT NULL,
  attempt           INTEGER NOT NULL,
  project           TEXT    NOT NULL,
  run_number        INTEGER NOT NULL,
  path              TEXT    NOT NULL,
  package           TEXT    NOT NULL DEFAULT '',
  lines_covered     {{BIG}} NOT NULL DEFAULT 0,
  lines_total       {{BIG}} NOT NULL DEFAULT 0,
  branches_covered  {{BIG}} NOT NULL DEFAULT 0,
  branches_total    {{BIG}} NOT NULL DEFAULT 0,
  functions_covered {{BIG}} NOT NULL DEFAULT 0,
  functions_total   {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX coverage_files_run ON coverage_files(run_id, step_id, attempt);
CREATE INDEX coverage_files_prune ON coverage_files(project, run_number);
`},

	// 19: verify steps (kind verify) and ai steps (kind ai); see verify.go
	//    and aistep.go. A verification's events (verify.started with the
	//    baseline and the resolved parameters, verify.interval with what a
	//    deploy runner read, verify.verdict, verify.override) are the
	//    truth; verifications is their projection for lists, and
	//    verify_baselines keeps what each passed verification saw, by
	//    environment, project and service, for later ones to compare with.
	//    ai_usage counts each project's calls and tokens per model and day
	//    (UTC) against the registry's budgets; ai_explanations keeps what a
	//    model said when someone asked it to explain a failed step.
	//    Compatibility: additive: new tables only. A run whose pipeline has
	//    a verify or ai step has min_schema 19 from creation (the pipeline
	//    carries "requires": 19), so older servers leave it alone; the
	//    four event types are of schema 19. Older servers ignore the tables.
	{additive, `
CREATE TABLE verifications (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  project     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  environment TEXT    NOT NULL,
  service     TEXT    NOT NULL,
  deployment  TEXT    NOT NULL DEFAULT '',
  target      TEXT    NOT NULL DEFAULT '',
  version     TEXT    NOT NULL DEFAULT '',
  outcome     TEXT    NOT NULL DEFAULT 'running',
  summary     TEXT    NOT NULL DEFAULT '',
  intervals   INTEGER NOT NULL DEFAULT 0,
  total       INTEGER NOT NULL DEFAULT 0,
  overridden_by TEXT  NOT NULL DEFAULT '',
  started_at  {{BIG}} NOT NULL,
  decided_at  {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, step_id)
);
CREATE INDEX verifications_env ON verifications(environment, project, service, started_at);

CREATE TABLE verify_baselines (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  environment TEXT    NOT NULL,
  project     TEXT    NOT NULL,
  service     TEXT    NOT NULL,
  run_number  INTEGER NOT NULL,
  version     TEXT    NOT NULL DEFAULT '',
  data        TEXT    NOT NULL,
  created_at  {{BIG}} NOT NULL,
  PRIMARY KEY (run_id, step_id)
);
CREATE INDEX verify_baselines_unit ON verify_baselines(environment, project, service, created_at);

CREATE TABLE ai_usage (
  project       TEXT    NOT NULL,
  model         TEXT    NOT NULL,
  day           TEXT    NOT NULL,
  calls         INTEGER NOT NULL DEFAULT 0,
  input_tokens  {{BIG}} NOT NULL DEFAULT 0,
  output_tokens {{BIG}} NOT NULL DEFAULT 0,
  cost_micros   {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (project, model, day)
);

CREATE TABLE ai_explanations (
  id            TEXT    PRIMARY KEY,
  run_id        TEXT    NOT NULL,
  step_id       TEXT    NOT NULL,
  attempt       INTEGER NOT NULL,
  project       TEXT    NOT NULL,
  model         TEXT    NOT NULL,
  actor         TEXT    NOT NULL,
  text          TEXT    NOT NULL,
  input_tokens  {{BIG}} NOT NULL DEFAULT 0,
  output_tokens {{BIG}} NOT NULL DEFAULT 0,
  cost_micros   {{BIG}} NOT NULL DEFAULT 0,
  created_at    {{BIG}} NOT NULL
);
CREATE INDEX ai_explanations_step ON ai_explanations(run_id, step_id, created_at);

ALTER TABLE steps ADD COLUMN kind TEXT NOT NULL DEFAULT '';
`},

	// 20: runner identities (see runnerids.go and internal/runnerauth). A
	//     join token, made by a person for one pool and stored hashed, is
	//     exchanged once (or up to max_uses times) for an identity: a
	//     server-assigned runner ID and the public half of a key the runner
	//     made. The runner then gets short-lived access tokens
	//     (runner_access_tokens, hashed) with assertions signed by its key;
	//     runner_assertions remembers each assertion's ID until it expires,
	//     so none is accepted twice by any server. runners gains what the
	//     runners list shows: the kind of runner (build or deploy), how it
	//     authenticated (identity, shared token or pool token), its labels,
	//     what its accept: section allows, and its OS and architecture.
	//     Compatibility: additive: new tables and new columns with defaults,
	//     and no new event type. Older servers never read the new tables, so
	//     they refuse runners' access tokens (enrolled runners work only
	//     with servers of schema 20 or newer); shared and pool tokens work
	//     with every server.
	{additive, `
CREATE TABLE runner_join_tokens (
  id           TEXT PRIMARY KEY,
  hash         TEXT    NOT NULL UNIQUE,
  pool         TEXT    NOT NULL,
  labels       TEXT    NOT NULL DEFAULT '',
  description  TEXT    NOT NULL DEFAULT '',
  max_uses     INTEGER NOT NULL DEFAULT 1,
  uses         INTEGER NOT NULL DEFAULT 0,
  ephemeral    INTEGER NOT NULL DEFAULT 0,
  created_by   TEXT    NOT NULL,
  created_at   {{BIG}} NOT NULL,
  expires_at   {{BIG}} NOT NULL,
  last_used_at {{BIG}} NOT NULL DEFAULT 0,
  revoked_by   TEXT    NOT NULL DEFAULT '',
  revoked_at   {{BIG}} NOT NULL DEFAULT 0
);

CREATE TABLE runner_identities (
  id             TEXT PRIMARY KEY,
  name           TEXT    NOT NULL,
  pool           TEXT    NOT NULL,
  public_key     TEXT    NOT NULL,
  key_created_at {{BIG}} NOT NULL,
  join_token     TEXT    NOT NULL DEFAULT '',
  enrolled_by    TEXT    NOT NULL DEFAULT '',
  enrolled_at    {{BIG}} NOT NULL,
  labels         TEXT    NOT NULL DEFAULT '',
  hostname       TEXT    NOT NULL DEFAULT '',
  os             TEXT    NOT NULL DEFAULT '',
  arch           TEXT    NOT NULL DEFAULT '',
  version        TEXT    NOT NULL DEFAULT '',
  executor       TEXT    NOT NULL DEFAULT '',
  ephemeral      INTEGER NOT NULL DEFAULT 0,
  last_seen      {{BIG}} NOT NULL DEFAULT 0,
  revoked_at     {{BIG}} NOT NULL DEFAULT 0,
  revoked_by     TEXT    NOT NULL DEFAULT '',
  revoke_reason  TEXT    NOT NULL DEFAULT '',
  rotate_asked   {{BIG}} NOT NULL DEFAULT 0
);
CREATE INDEX runner_identities_pool ON runner_identities(pool);

CREATE TABLE runner_access_tokens (
  hash       TEXT PRIMARY KEY,
  runner     TEXT    NOT NULL,
  expires_at {{BIG}} NOT NULL
);
CREATE INDEX runner_access_tokens_runner ON runner_access_tokens(runner);
CREATE INDEX runner_access_tokens_expires ON runner_access_tokens(expires_at);

CREATE TABLE runner_assertions (
  id         TEXT PRIMARY KEY,
  expires_at {{BIG}} NOT NULL
);
CREATE INDEX runner_assertions_expires ON runner_assertions(expires_at);

ALTER TABLE runners ADD COLUMN kind TEXT NOT NULL DEFAULT '';
ALTER TABLE runners ADD COLUMN auth TEXT NOT NULL DEFAULT '';
ALTER TABLE runners ADD COLUMN labels TEXT NOT NULL DEFAULT '';
ALTER TABLE runners ADD COLUMN accept TEXT NOT NULL DEFAULT '';
ALTER TABLE runners ADD COLUMN os TEXT NOT NULL DEFAULT '';
ALTER TABLE runners ADD COLUMN arch TEXT NOT NULL DEFAULT '';
`},

	// 21: runner pools for build steps (see pools.go). A pool serves the
	//     projects (and, if it says, the events) its configuration names;
	//     a runner of a pool is only ever given steps of runs its pool
	//     serves. A pipeline may choose a pool and labels for its runs
	//     (runner: { pool, labels }), kept in runs.runner_pool and
	//     runs.runner_labels so that leasing filters on them in SQL.
	//     Compatibility: additive: new columns with defaults, and no new
	//     event type. Older servers do not know pools and would hand any
	//     run's steps to any runner, so runs that a pool decides have
	//     min_schema 21: a pipeline that chooses runners carries
	//     "requires": 21, and while any build pool is configured every new
	//     run's run.created event does. Older servers leave such runs alone.
	{additive, `
ALTER TABLE runs ADD COLUMN runner_pool TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN runner_labels TEXT NOT NULL DEFAULT '';
`},

	// 22: step resources (see resources.go). A step may ask for CPU and
	//     memory (the pipeline key resources); what it leaves out comes from
	//     the installation's default for its kind, and what it may ask for
	//     is bounded by the installation's limits. Both are configuration,
	//     so both are tables: step_resource_defaults, one row per step kind,
	//     and step_resource_limits, one row for the installation. Quantities
	//     are kept as the text a pipeline writes (2Gi, 500m). A server seeds
	//     both tables with the values compiled into it when it finds them
	//     empty; after that the rows are the configuration.
	//     Compatibility: additive: two new tables and nothing else, no new
	//     event type and nothing older code reads. Older servers do not
	//     size containers, so runs get the namespace's defaults there, as
	//     before; the pipeline key resources is one they refuse (a pipeline
	//     that uses it carries "requires": 22, so they leave such runs alone).
	{additive, `
CREATE TABLE step_resource_defaults (
  kind         TEXT PRIMARY KEY,
  cpu          TEXT    NOT NULL DEFAULT '',
  cpu_limit    TEXT    NOT NULL DEFAULT '',
  memory       TEXT    NOT NULL DEFAULT '',
  memory_limit TEXT    NOT NULL DEFAULT '',
  updated_at   {{BIG}} NOT NULL DEFAULT 0,
  updated_by   TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE step_resource_limits (
  name              TEXT PRIMARY KEY,
  max_cpu           TEXT    NOT NULL DEFAULT '',
  max_memory        TEXT    NOT NULL DEFAULT '',
  memory_per_thread TEXT    NOT NULL DEFAULT '',
  updated_at        {{BIG}} NOT NULL DEFAULT 0,
  updated_by        TEXT    NOT NULL DEFAULT ''
);
`}, {additive, schema23SQL}, // 23: configuration in the database (db_config.go)
}

// schemaIdentity is the schema that brought runner identities.
const schemaIdentity = 20

// schemaPools is the schema that brought runner pools for build steps.
const schemaPools = 21

// schemaPolicy is the schema that brought delivery policies.
const schemaPolicy = 16

// schemaBlobs is the schema that brought the built-in blob store and
// artifact_blobs.store.
const schemaBlobs = 17

// schemaReports is the schema that brought richer test results, test
// attachments, coverage and HTML reports.
const schemaReports = 18

// schemaVerify is the schema that brought verify and ai steps.
const schemaVerify = 19

// schemaImages is the schema that brought image steps.
const schemaImages = 15

// schemaControl is the schema that brought commands on deployments, rollout
// steps, and observing idle services.
const schemaControl = 14

// schemaDeploy is the schema that brought deploy and rollback steps.
const schemaDeploy = 11

// schemaTriggers is the schema that brought run inputs, loops, schedules,
// webhooks and limited API tokens.
const schemaTriggers = 13

// schemaTests is the schema that brought test reports.
const schemaTests = 10

// schemaArtifacts is the schema that brought artifacts, security findings
// and SBOMs.
const schemaArtifacts = 12

// schemaPullRequests is the schema that brought pull-request runs,
// superseding, and the columns that record why a run was cancelled.
const schemaPullRequests = 9

// schemaReruns is the schema that brought step inputs, re-runs and
// runs.min_schema. Code that uses its tables and columns checks that the
// build knows it (Engine.knows), so that a build limited to older schemas
// for testing (see schemaLimit) behaves as an older release would.
const schemaReruns = 8

// schemaLimit, if set when building (-ldflags "-X
// github.com/jk-magic-creations/launchway/internal/engine.schemaLimit=7"),
// makes the build behave as one that knows only the first schemaLimit
// migrations and the events they brought. It exists to test upgrades and
// their rollback (docs/development/migrations.md); never set it in a release.
var schemaLimit string

// knownSchema is the newest schema this build knows.
func knownSchema() (int, error) {
	if schemaLimit == "" {
		return len(migrations), nil
	}
	n, err := strconv.Atoi(schemaLimit)
	if err != nil || n < 1 || n > len(migrations) {
		return 0, fmt.Errorf("this build was made with schemaLimit %q; it must be between 1 and %d", schemaLimit, len(migrations))
	}
	return n, nil
}

// minimumFor is the oldest code schema that can run against a database at
// schema v: the newest breaking migration up to v.
func minimumFor(v int) int {
	m := 0
	for i := 0; i < v && i < len(migrations); i++ {
		if migrations[i].compat == breaking {
			m = i + 1
		}
	}
	return m
}

// readSchema reads the database's schema version and minimum. A database
// last migrated before schema 8 has no minimum stored; it is worked out.
func readSchema(q interface {
	QueryRow(string, ...any) *rowx
}) (SchemaInfo, error) {
	var info SchemaInfo
	for key, dst := range map[string]*int{"schema_version": &info.Database, "schema_min": &info.Minimum} {
		var v string
		err := q.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			*dst = -1
			continue
		}
		if err != nil {
			return info, err
		}
		if *dst, err = strconv.Atoi(v); err != nil {
			return info, fmt.Errorf("meta %s: %q is not a number", key, v)
		}
	}
	if info.Database < 0 {
		info.Database = 0
	}
	if info.Minimum < 0 {
		info.Minimum = minimumFor(info.Database)
		if info.Database > len(migrations) {
			info.Minimum = info.Database // unknown: assume the worst
		}
	}
	return info, nil
}

// migrate brings the database up to the schema this build knows, unless it
// is newer already. Each migration is applied in a transaction of its own
// that checks the version again, so it is applied exactly once; on
// PostgreSQL servers starting together also take turns under an advisory
// lock, so the others wait for the one migrating.
//
// A database newer than this build is fine if the build is at least its
// minimum (every migration since is additive): it is logged once. Otherwise
// migrate refuses, saying which schema is needed.
func migrate(d *dbx, known int, log *slog.Logger) (SchemaInfo, error) {
	ctx := context.Background()
	if d.pg {
		// Servers starting together take turns.
		conn, err := d.Conn(ctx)
		if err != nil {
			return SchemaInfo{}, err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7123401)`); err != nil {
			return SchemaInfo{}, err
		}
		defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7123401)`)
	}
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return SchemaInfo{}, err
	}
	for {
		applied, err := migrateOne(d, known)
		if err != nil {
			return SchemaInfo{}, err
		}
		if applied == 0 {
			break
		}
		log.Info("applied a database migration", "schema", applied, "compatibility", migrations[applied-1].compat.String(),
			"minimum", minimumFor(applied))
	}
	info, err := readSchema(d)
	if err != nil {
		return info, err
	}
	info.Code = known
	if msg := info.Refusal(); msg != "" {
		return info, errors.New(msg)
	}
	if info.Database > known {
		log.Warn("running against a database written by a newer version of Launchway; every migration since this build's is additive, so it can run, "+
			"but runs that use what it does not know are left to newer servers",
			"database_schema", info.Database, "code_schema", known, "minimum", info.Minimum)
	}
	return info, nil
}

// migrateOne applies the next migration, if this build knows one the
// database lacks, and returns its number; 0 if there was none to apply.
func migrateOne(d *dbx, known int) (int, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	info, err := readSchema(tx)
	if err != nil {
		return 0, err
	}
	if info.Database >= known {
		return 0, nil
	}
	i := info.Database
	for _, stmt := range strings.Split(render(d.pg, migrations[i].sql), ";\n") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			return 0, fmt.Errorf("migration %d: %w\n%s", i+1, err, strings.TrimSpace(stmt))
		}
	}
	for key, v := range map[string]int{"schema_version": i + 1, "schema_min": max(info.Minimum, minimumFor(i+1))} {
		if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, strconv.Itoa(v)); err != nil {
			return 0, err
		}
	}
	return i + 1, tx.Commit()
}

// isConflict reports whether err means another transaction got there
// first, so the work should be retried against fresh state.
func isConflict(err error) bool { return store.IsConflict(err) }
