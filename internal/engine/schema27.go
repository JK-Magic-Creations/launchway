package engine

// Schema 27, the second set of pipeline parity features, kept in a file of
// its own: db.go is over the size ratchet.
//
// The file is named so that its init runs after schema26.go's: the
// migrations are appended in file-name order.

// schemaParity2 is the schema of this branch's features: fetching from the
// members of a loop, a step with a runner and a workspace of its own,
// service readiness and service logs, approvers, and templates that take
// secret references and hold verify, ai and approval steps.
const schemaParity2 = 27

// 27: a step that chooses its own runner (see pipeline.StepRunner): its
//
//	selector is kept on the step's row so that every server's leasing
//	can match it in SQL (runner_pool, runner_labels: the labels joined
//	by commas, in order), and own_workspace says that the step has a
//	workspace of its own and does not take over the run's runner. An
//	approval that asks for several approvals keeps how many it needs
//	and who approved so far as JSON in approvals, for the views and
//	the list of waiting approvals; the events are the record. The
//	pipeline keys of the stories of this schema (fetch from a loop, a
//	step's runner, approvers, service readiness, secret references in
//	templates) carry "requires": 27 and the events that use new fields
//	carry Requires, so older servers leave such runs alone.
//	Compatibility: additive: new columns with defaults.
var migrationParity2 = migration{additive, `
ALTER TABLE steps ADD COLUMN own_workspace INTEGER NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN runner_pool TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN runner_labels TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN approvals TEXT NOT NULL DEFAULT '';
`}

func init() {
	if len(migrations) != schemaParity2-1 {
		panic("the parity migration (27) must follow migrations 1 to 26")
	}
	migrations = append(migrations, migrationParity2)
}
