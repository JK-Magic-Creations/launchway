package engine

import "github.com/jk-magic-creations/launchway/internal/pipeline"

// Schema 27, the second set of pipeline parity features, kept in a file of
// its own: db.go is over the size ratchet.
//
// Schemas 23 (the configuration kept in the database), 25 (caches) and 26
// (Git hosting) are built on other branches. The migrations here stand in
// for 25 and 26 so that 27 is the number it was given; when those branches
// merge, their real migrations take the stand-ins' place in the list and
// migration25Stand, migration26Stand and their line in init are deleted
// (23's stand-in is in migrations_flow.go).

// schemaParity2 is the schema of this branch's features: fetching from the
// members of a loop, a step with a runner and a workspace of its own,
// service readiness and service logs, approvers, and templates that take
// secret references and hold verify, ai and approval steps.
const schemaParity2 = 27

// 25: reserved for caches (built on another branch); empty here.
//
//	Compatibility: additive: nothing.
var migration25Stand = migration{additive, ``}

// 26: reserved for the Git hosting work (built on another branch); empty
//
//	here.
//	Compatibility: additive: nothing.
var migration26Stand = migration{additive, ``}

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
	migrations = append(migrations, migration25Stand, migration26Stand, migrationParity2)
}

// checkNewerSchemas raises the run's schema to the newest the pipeline
// needs and marks the run as not understood if this server is older: flow
// control (24: when.output, on_failure, retry waits, timeout, lane or into)
// and the keys of schema 27 (see pipeline.UsesSchemaParity2).
func (st *RunState) checkNewerSchemas(p *pipeline.Pipeline) {
	if p.UsesSchemaFlow() {
		st.Schema = max(st.Schema, schemaFlow)
		if !st.understands(schemaFlow) {
			st.notUnderstood("a pipeline key of schema %d (flow control: when.output, on_failure, retry waits, timeout, lane or into)", schemaFlow)
		}
	}
	if p.UsesSchemaParity2() {
		st.Schema = max(st.Schema, schemaParity2)
		if !st.understands(schemaParity2) {
			st.notUnderstood("a pipeline key of schema %d (fetch from a loop, a step's own runner, approvers, service readiness)", schemaParity2)
		}
	}
}
