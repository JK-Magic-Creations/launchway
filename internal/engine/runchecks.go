package engine

import (
	"fmt"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

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
	if ps := e.CheckStepRunners(nr.Project, nr.Trigger.Event, nr.Pipeline); len(ps) > 0 {
		return ps
	}
	if ps := e.CheckResources(nr.Pipeline); len(ps) > 0 {
		return ps
	}
	if ps := e.CheckRunLimits(nr.Pipeline); len(ps) > 0 {
		return ps
	}
	return nil
}

// checkPullRequest checks the trigger of a pull-request run.
func checkPullRequest(t Trigger) error {
	if t.PullRequest <= 0 || t.Ref != PullRequestRef(t.PullRequest) || t.Branch != "" || t.Tag != "" {
		return fmt.Errorf("%w: a pull-request run needs the pull request's number and its ref, refs/pull/<number>/head", ErrConflict)
	}
	return nil
}

// PullRequestRef is the ref a Git host keeps a pull request's head at.
func PullRequestRef(number int) string { return "refs/pull/" + strconv.Itoa(number) + "/head" }

// checkLibraries checks that a pipeline is ready to run and that the source
// says where to fetch every library it was expanded from, at the same commit.
func checkLibraries(p *pipeline.Pipeline, src Source) error {
	if !p.Expanded() {
		return fmt.Errorf("%w: the pipeline uses templates that have not been expanded; "+
			"templates come from libraries the server knows, so run it on a server", ErrConflict)
	}
	for _, l := range p.Libraries {
		found := false
		for _, ls := range src.Libraries {
			if ls.Name == l.Name {
				found = ls.Commit == l.Commit && pipeline.IsCommitID(ls.Commit) && ls.URL != ""
			}
		}
		if !found {
			return fmt.Errorf("%w: the source does not say where to fetch library %s at %s", ErrConflict, l.Name, l.Commit)
		}
	}
	if len(src.Libraries) > 0 && src.Kind != "git" {
		return fmt.Errorf("%w: libraries are only for runs from a Git repository", ErrConflict)
	}
	return nil
}
