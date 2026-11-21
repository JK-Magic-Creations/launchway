package engine

// Schema 29, variables at levels (see internal/settings), kept in a file of
// its own: db.go is over the size ratchet. It is named so that its init runs
// after schema28.go's: the migrations are appended in file-name order.

// schemaVariables is the schema that brought variables at levels.
const schemaVariables = 29

func init() {
	if len(migrations) != schemaVariables-1 {
		panic("the variables migration (29) must follow migrations 1 to 28")
	}
	migrations = append(migrations, variablesMigration)
}

// 29: variables at levels. A variable is a name with a type and a value,
//
//	set at a level (installation, organization, unit or project; scope is
//	the unit's or the project's name, empty for the two above), for
//	everything or for one environment. A nearer level overrides it; a lock
//	set at a level binds everything below. variables holds what is set
//	now; variable_changes is the audit of every change (who, the old and
//	the new value as text, never the content a secret reference points
//	at, which Launchway does not read here); run_variables is what each
//	run resolved when it was created, so a re-run repeats it and the run
//	page can show it. org keeps variables from crossing organizations.
//	Compatibility: additive: new tables and nothing else. Nothing older
//	code reads changes; a pipeline that binds a variable (from:) carries
//	"requires": 29, so older servers leave such runs alone.
//	Downgrade: nothing to undo for an older server. The three tables stay
//	and are ignored; drop them only if the feature is abandoned.
var variablesMigration = migration{additive, `
CREATE TABLE variables (
  org         TEXT    NOT NULL DEFAULT 'default',
  level       TEXT    NOT NULL,
  scope       TEXT    NOT NULL DEFAULT '',
  name        TEXT    NOT NULL,
  environment TEXT    NOT NULL DEFAULT '',
  type        TEXT    NOT NULL,
  of_type     TEXT    NOT NULL DEFAULT '',
  value       TEXT    NOT NULL DEFAULT 'null',
  description TEXT    NOT NULL DEFAULT '',
  locked      INTEGER NOT NULL DEFAULT 0,
  lock_reason TEXT    NOT NULL DEFAULT '',
  locked_by   TEXT    NOT NULL DEFAULT '',
  locked_at   {{BIG}} NOT NULL DEFAULT 0,
  version     INTEGER NOT NULL DEFAULT 1,
  created_at  {{BIG}} NOT NULL DEFAULT 0,
  created_by  TEXT    NOT NULL DEFAULT '',
  updated_at  {{BIG}} NOT NULL DEFAULT 0,
  updated_by  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (org, level, scope, name, environment)
);

CREATE TABLE variable_changes (
  id          {{ID}},
  org         TEXT    NOT NULL DEFAULT 'default',
  at          {{BIG}} NOT NULL,
  actor       TEXT    NOT NULL DEFAULT '',
  level       TEXT    NOT NULL,
  scope       TEXT    NOT NULL DEFAULT '',
  name        TEXT    NOT NULL,
  environment TEXT    NOT NULL DEFAULT '',
  action      TEXT    NOT NULL,
  old_value   TEXT    NOT NULL DEFAULT '',
  new_value   TEXT    NOT NULL DEFAULT '',
  detail      TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX variable_changes_by_name ON variable_changes (org, name, id);

CREATE TABLE run_variables (
  run_id      TEXT    NOT NULL,
  name        TEXT    NOT NULL,
  environment TEXT    NOT NULL DEFAULT '',
  type        TEXT    NOT NULL,
  value       TEXT    NOT NULL DEFAULT '',
  level       TEXT    NOT NULL DEFAULT '',
  scope       TEXT    NOT NULL DEFAULT '',
  locked      INTEGER NOT NULL DEFAULT 0,
  chain       TEXT    NOT NULL DEFAULT '[]',
  used_by     TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (run_id, name)
)
`}
