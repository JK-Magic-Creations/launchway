package engine

// Schema 54: what each step attempt really used (automatic resources, see
// docs/development/automatic-resources.md). It is number 54, after inspect
// requests (53).

// schemaUsage is the schema that brought the measured use of a step attempt.
const schemaUsage = 54

func init() {
	if len(migrations) != schemaUsage-1 {
		panic("migration 54 must follow migrations 1 to 53")
	}
	migrations = append(migrations, usageMigration)
}

// 54: step_usage, one row per step attempt that was measured or tried to be.
//
//	measured says whether the numbers are real: 0 is "not measured" (the
//	executor has no cgroup to read, or reading it failed) and the numbers are
//	then 0 and meaningless; note says why. memory_peak is the most memory the
//	step's container used, in bytes; cpu_peak_milli the most CPU it used over
//	any sampled second, in thousandths of a CPU; cpu_ms the CPU time it used
//	in all, in milliseconds. reserved_memory and reserved_cpu_milli are what
//	was reserved for the container the step ran in (a container may be shared
//	by several steps of the run), 0 when nothing was reserved. method names
//	how it was read: cgroup2, cgroup1 or sampled.
//	Compatibility: additive: one new table, ignored by an older server.
//	Downgrade: nothing to undo.
var usageMigration = migration{additive, `
CREATE TABLE step_usage (
  run_id             TEXT    NOT NULL,
  step_id            TEXT    NOT NULL,
  attempt            INTEGER NOT NULL,
  measured           INTEGER NOT NULL DEFAULT 0,
  method             TEXT    NOT NULL DEFAULT '',
  note               TEXT    NOT NULL DEFAULT '',
  memory_peak        {{BIG}} NOT NULL DEFAULT 0,
  cpu_peak_milli     {{BIG}} NOT NULL DEFAULT 0,
  cpu_ms             {{BIG}} NOT NULL DEFAULT 0,
  reserved_memory    {{BIG}} NOT NULL DEFAULT 0,
  reserved_cpu_milli {{BIG}} NOT NULL DEFAULT 0,
  runner             TEXT    NOT NULL DEFAULT '',
  created_at         {{BIG}} NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, step_id, attempt)
);
`}
