package engine

// Schema 32, rollback after a successful deployment (see rollbacks.go), kept
// in a file of its own: db.go is over the size ratchet. It is named so that
// its init runs after schema31.go's: the migrations are appended in
// file-name order.

// schemaRollback is the schema that brought rollback runs.
const schemaRollback = 32

func init() {
	if len(migrations) != schemaRollback-1 {
		panic("the rollback migration (32) must follow migrations 1 to 31")
	}
	migrations = append(migrations, rollbackMigration)
}

// 32: rollback after success. A rollback is a run of its own (trigger event
//
//	rollback) that restores an earlier deployment's version of an
//	environment's service. rollbacks has one row per such run, written when
//	the run is created and when a person acknowledges it: what was asked and
//	by whom, why, the deployment it restores, the one that ran, whether it
//	needs review (an emergency on a protected environment) and who
//	acknowledged it. rollback_reverts lists the deployments the run undoes,
//	which is how the run of an undone deployment says "rolled back later by
//	run #N", and how a unit's history knows what is still in effect.
//	Compatibility: additive: new tables and nothing else. A server that does
//	not know them never reads them; it cannot fold a rollback run (the event
//	run.reviewed and the pipeline's restore key are newer than it), so it
//	leaves such runs alone (runs.min_schema 32).
//	Downgrade: nothing to undo for an older server. The tables stay and are
//	ignored; drop them only if the feature is abandoned.
var rollbackMigration = migration{additive, `
CREATE TABLE rollbacks (
  run_id          TEXT    PRIMARY KEY,
  project         TEXT    NOT NULL,
  run_number      INTEGER NOT NULL,
  environment     TEXT    NOT NULL,
  service         TEXT    NOT NULL,
  deployment      TEXT    NOT NULL DEFAULT '',
  to_deployment   TEXT    NOT NULL,
  to_version      TEXT    NOT NULL,
  to_run_id       TEXT    NOT NULL DEFAULT '',
  to_run_number   INTEGER NOT NULL DEFAULT 0,
  from_deployment TEXT    NOT NULL DEFAULT '',
  from_version    TEXT    NOT NULL DEFAULT '',
  reason          TEXT    NOT NULL,
  actor           TEXT    NOT NULL,
  emergency       INTEGER NOT NULL DEFAULT 0,
  review_required INTEGER NOT NULL DEFAULT 0,
  reviewed_by     TEXT    NOT NULL DEFAULT '',
  reviewed_at     {{BIG}} NOT NULL DEFAULT 0,
  review_comment  TEXT    NOT NULL DEFAULT '',
  created_at      {{BIG}} NOT NULL
);

CREATE INDEX rollbacks_by_unit ON rollbacks (project, environment, service, created_at);
CREATE INDEX rollbacks_review ON rollbacks (review_required, reviewed_at);

CREATE TABLE rollback_reverts (
  run_id     TEXT    NOT NULL,
  deployment TEXT    NOT NULL,
  position   INTEGER NOT NULL,
  PRIMARY KEY (run_id, deployment)
);

CREATE INDEX rollback_reverts_by_deployment ON rollback_reverts (deployment)
`}
