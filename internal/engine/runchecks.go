package engine

import "fmt"

// checkSchemas refuses a run whose pipeline uses what this server, or a build
// limited to an older schema, does not know, and what the installation's
// configuration does not allow.
func (e *Engine) checkSchemas(nr *NewRun) error {
	// What each schema brought, in order: a server, or a build limited to
	// an older schema for testing, refuses what it does not know.
	if nr.Pipeline.UsesSchema10() && !e.knows(schemaTests) {
		// A release that old cannot even read the pipeline; a build limited
		// to it for testing refuses in the same place.
		return fmt.Errorf("%w: this server does not know test reports (schema %d)", ErrConflict, schemaTests)
	}
	if nr.Pipeline.UsesSchema11() {
		if !e.knows(schemaDeploy) {
			return fmt.Errorf("%w: this server does not know deploy steps (schema %d)", ErrConflict, schemaDeploy)
		}
		if ps := e.CheckDeploys(nr.Project, nr.Pipeline); len(ps) > 0 {
			return ps
		}
	}
	if nr.Pipeline.UsesSchema12() && !e.knows(schemaArtifacts) {
		return fmt.Errorf("%w: this server does not know artifacts, fetch or findings (schema %d)", ErrConflict, schemaArtifacts)
	}
	if nr.Pipeline.UsesSchema15() && !e.knows(schemaImages) {
		return fmt.Errorf("%w: this server does not know image steps or deploy changes that pin a digest (schema %d)", ErrConflict, schemaImages)
	}
	if nr.Pipeline.UsesSchema18() && !e.knows(schemaReports) {
		return fmt.Errorf("%w: this server does not know test attachments, coverage or HTML reports (schema %d)", ErrConflict, schemaReports)
	}
	if nr.Pipeline.UsesSchemaVerify() {
		if !e.knows(schemaVerify) {
			return fmt.Errorf("%w: this server does not know verify or ai steps (schema %d)", ErrConflict, schemaVerify)
		}
		if ps := e.CheckVerify(nr.Project, nr.Pipeline); len(ps) > 0 {
			return ps
		}
	}
	if nr.Pipeline.UsesSchemaPools() && !e.knows(schemaPools) {
		return fmt.Errorf("%w: this server does not know runner pools (runner:, schema %d)", ErrConflict, schemaPools)
	}
	if nr.Pipeline.UsesSchemaResources() {
		if !e.knows(schemaResources) {
			return fmt.Errorf("%w: this server does not know step resources (resources:, schema %d)", ErrConflict, schemaResources)
		}
	}
	if nr.Pipeline.UsesSchemaFlow() && !e.knows(schemaFlow) {
		return fmt.Errorf("%w: this server does not know flow control keys (when.output, on_failure, retry waits, timeout, lane or into, schema %d)", ErrConflict, schemaFlow)
	}
	if nr.Pipeline.UsesSchemaParity2() && !e.knows(schemaParity2) {
		return fmt.Errorf("%w: this server does not know what schema %d brought (fetch from a loop, a step's own runner, approvers, service readiness, secret references in templates)", ErrConflict, schemaParity2)
	}
	// Step resources above the installation's limits could never be given;
	// say so now, naming the limit, instead of failing the step later.
	if ps := e.CheckResources(nr.Pipeline); len(ps) > 0 {
		return ps
	}
	if ps := e.CheckRunLimits(nr.Pipeline); len(ps) > 0 {
		return ps
	}
	return nil
}
