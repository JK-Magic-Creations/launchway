package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// checkRun checks what a new run asks for against what this server holds: its libraries, the schemas its
// features need, its inputs, and (for a rollback run) the rollback itself.
func (e *Engine) checkRun(nr *NewRun) error {
	if err := checkLibraries(nr.Pipeline, nr.Source); err != nil {
		return err
	}
	if err := e.checkSchemas(nr); err != nil {
		return err
	}
	if err := e.checkNewRun(nr); err != nil { // schema 13 and the run's inputs
		return err
	}
	return e.checkRollbackRun(nr)
}

// draftOf makes the draft a draft run carries, and refuses the combinations that make no sense.
func draftOf(nr *NewRun) (*Draft, error) {
	switch {
	case nr.Draft != nil:
		if nr.Push != nil {
			return nil, fmt.Errorf("%w: a draft run is never started by a push", ErrConflict)
		}
		nr.Trigger.Event = pipeline.EventDraft
		return &Draft{Text: nr.Draft.Text, Hash: DraftHash(nr.Draft.Text), Secrets: nr.Draft.Secrets}, nil
	case nr.Trigger.Event == pipeline.EventDraft:
		return nil, fmt.Errorf("%w: a draft run needs the text it runs", ErrConflict)
	}
	return nil, nil
}

// pullRequestSecrets checks a pull-request run and says whether it may use secrets (nil for any other run).
func (e *Engine) pullRequestSecrets(nr *NewRun) (*bool, error) {
	if nr.Trigger.Event != pipeline.EventPullRequest {
		return nil, nil
	}
	if !e.knows(schemaPullRequests) {
		return nil, fmt.Errorf("%w: this server does not know pull-request runs (schema %d)", ErrConflict, schemaPullRequests)
	}
	if err := checkPullRequest(nr.Trigger); err != nil {
		return nil, err
	}
	// Servers older than schema 9 do not know on.pull_request, so they
	// leave such a run alone instead of running it with secrets.
	if !nr.Pipeline.On.MatchPullRequest() {
		return nil, fmt.Errorf("%w: a pull-request run's pipeline must ask for pull requests (on: { pull_request: {} })", ErrConflict)
	}
	return &nr.PullRequestSecrets, nil
}

// advanceOwn advances a pending step that is not started by the usual rule: a rollback run's restore step, or a
// rollback step (which runs because a step it needs did not succeed). It sets *changed when the step moved, and
// says whether the step was one of those.
func (c *cmd) advanceOwn(id string, def *pipeline.Step, changed *bool) (handled bool) {
	switch {
	case def.IsRestore():
		*changed = c.advanceRestore(id, def) || *changed
	case def.IsRollback():
		*changed = c.advanceRollback(id, def) || *changed
	case def.IsGate():
		*changed = c.advanceGate(id, def) || *changed
	default:
		return false
	}
	return true
}

// policyDue reports whether the run's policies are to be checked by this command: it created, re-ran or resumed
// the run. A rollback run is an emergency action and is never held by the policies of an ordinary run.
func (c *cmd) policyDue() bool {
	return c.startedOrResumed() && c.st.Trigger.Event != pipeline.EventRollback
}
