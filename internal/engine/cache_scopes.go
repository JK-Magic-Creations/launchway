package engine

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// ---- scopes ----

// SetDefaultBranch tells the engine how to find a project's default branch,
// which decides whose cache entries are the default scope. The engine knows
// projects only by name; the server that knows their repositories sets this
// once, before it takes runs. Without it, or for a project it cannot say the
// default branch of, no run writes the default scope: guessing main would let
// a run of a branch that is not the default plant what every other run
// restores.
func (e *Engine) SetDefaultBranch(fn func(project string) string) {
	e.caches.defaultBranch.Store(&fn)
}

// projectDefaultBranch is the project's default branch, or empty when it is
// not known.
func (e *Engine) projectDefaultBranch(project string) string {
	if fn := e.caches.defaultBranch.Load(); fn != nil {
		return (*fn)(project)
	}
	return ""
}

// cacheScopes says which scopes a run reads, in the order it reads them, and
// which one it writes (empty if none, with why).
type cacheScopes struct {
	read    []string
	write   string
	whyNone string // why nothing is written
}

func (s cacheScopes) canRead(scope string) bool { return slices.Contains(s.read, scope) }

// scopesOf works out the scopes of a run from what the server recorded of
// how it started. Nothing a runner says takes part.
func (e *Engine) scopesOf(st *RunState) cacheScopes {
	t := st.Trigger
	def := e.projectDefaultBranch(st.Project) // empty if unknown: nothing then matches it
	var s cacheScopes
	switch {
	case st.Draft != nil || t.Event == pipeline.EventDraft:
		// Unreviewed text: it reads what the default branch built and saves
		// nothing, so that whatever it writes cannot reach anything else.
		s.read = []string{cacheScopeDefault}
		s.whyNone = "a draft run saves no cache; it restores the default branch's"
	case t.Event == pipeline.EventPullRequest:
		if t.PullRequest > 0 {
			own := cacheScopePull + strconv.Itoa(t.PullRequest)
			s.read, s.write = []string{own, cacheScopeDefault}, own
		} else {
			s.read = []string{cacheScopeDefault}
			s.whyNone = "a pull-request run without a pull request number saves no cache"
		}
	case t.Tag != "":
		own := cacheScopeRef + nonEmpty(t.Ref, "refs/tags/"+t.Tag)
		s.read, s.write = []string{own, cacheScopeDefault}, own
	case t.Branch != "" && t.Branch == def && (t.Ref == "" || t.Ref == "refs/heads/"+def):
		s.read, s.write = []string{cacheScopeDefault}, cacheScopeDefault
	case t.Branch != "":
		own := cacheScopeRef + nonEmpty(t.Ref, "refs/heads/"+t.Branch)
		s.read, s.write = []string{own, cacheScopeDefault}, own
	case t.Ref != "":
		own := cacheScopeRef + t.Ref
		s.read, s.write = []string{own, cacheScopeDefault}, own
	default:
		s.read = []string{cacheScopeDefault}
		s.whyNone = "this run was not started from a branch, tag or pull request, so it saves no cache"
	}
	return s
}

// cacheContext is what every runner call about a cache is checked against:
// the lease's run, step and attempt, the declared cache, and the scopes.
type cacheContext struct {
	l      leaseRef
	st     *RunState
	def    *pipeline.Step
	scopes cacheScopes
	limits cacheLimits
}

// cacheFor checks a runner's call: the lease is live, the step declares a
// cache of that name, and the server knows caches. It renews the lease, as
// an upload does.
func (e *Engine) cacheFor(leaseID, name string) (*cacheContext, error) {
	if !e.knows(schemaCache) {
		return nil, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	l, err := e.leaseFor(leaseID)
	if err != nil {
		return nil, err
	}
	st, err := e.runState(l.runID)
	if err != nil {
		return nil, err
	}
	def := st.Pipeline.Step(l.stepID)
	if def == nil {
		return nil, ErrLeaseLost
	}
	if name != "" {
		found := false
		for _, c := range def.Cache {
			found = found || c.Name == name
		}
		if !found {
			return nil, fmt.Errorf("%w: step %s declares no cache %q", ErrInvalid, l.stepID, name)
		}
	}
	if st.Project == "" {
		return nil, fmt.Errorf("%w: this run belongs to no project, so it has no cache", ErrInvalid)
	}
	limits, err := e.readCacheLimits(e.db, st.Project)
	if err != nil {
		return nil, err
	}
	return &cacheContext{l: l, st: st, def: def, scopes: e.scopesOf(st), limits: limits}, nil
}
