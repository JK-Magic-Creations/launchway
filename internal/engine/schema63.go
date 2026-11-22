package engine

// Schema 63: custom steps (see internal/stepkit and docs/guide/write-your-own-step.md).

// schemaCustomSteps is the schema that brought the record of custom step uses.
const schemaCustomSteps = 63

func init() {
	if len(migrations) != schemaCustomSteps-1 {
		panic("migration 63 must follow migrations 1 to 62")
	}
	migrations = append(migrations, customStepsMigration)
}

// 63: custom_step_uses, one row for every step of a run that came from a
//
//	custom step: which step (name), from which library at which commit
//	(library "self" is a step of the run's own repository, at the run's
//	commit), the sha256 of its manifest, and what the manifest asked to be
//	allowed (network hosts, writable paths, secret inputs, each a comma-
//	separated list), with the project and the time, so that a step's page can
//	say who uses it and an audit can say which version of which step every
//	run ran. The summaries and annotations a step writes are kept in the
//	event that ends its attempt, as caches are, and need no table.
//	Compatibility: additive: one new table, ignored by an older server (which
//	does not run pipelines that use custom steps: they require schema 63).
//	Downgrade: nothing to undo.
var customStepsMigration = migration{additive, `
CREATE TABLE custom_step_uses (
  run_id      TEXT    NOT NULL,
  step_id     TEXT    NOT NULL,
  project     TEXT    NOT NULL DEFAULT '',
  name        TEXT    NOT NULL,
  library     TEXT    NOT NULL DEFAULT '',
  commit_sha  TEXT    NOT NULL DEFAULT '',
  digest      TEXT    NOT NULL DEFAULT '',
  network     TEXT    NOT NULL DEFAULT '',
  write_paths TEXT    NOT NULL DEFAULT '',
  secrets     TEXT    NOT NULL DEFAULT '',
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, step_id)
);
CREATE INDEX custom_step_uses_step ON custom_step_uses(library, name, commit_sha);
CREATE INDEX custom_step_uses_project ON custom_step_uses(project, created_at);
`}
