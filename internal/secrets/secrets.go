// Package secrets looks up secret values on a runner.
//
// The server never sees a secret value. Pipelines hold references
// (pipeline.SecretRef); the runner resolves them through the stores
// configured on it, just before a step runs, and masks the values in the
// step's output.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// Store reads secret values. Errors are *NotFoundError or *UnavailableError,
// and never contain a value.
type Store interface {
	Get(ctx context.Context, key, field string) (string, error)
}

// NotFoundError means the secret does not exist or may not be read. The
// step fails.
type NotFoundError struct{ Reason string }

func (e *NotFoundError) Error() string { return e.Reason }

// UnavailableError means the store could not answer: it is unreachable,
// sealed or failing. Trying again later may work, so the step is run again.
type UnavailableError struct{ Reason string }

func (e *UnavailableError) Error() string { return e.Reason }

func notFound(format string, a ...any) error {
	return &NotFoundError{Reason: fmt.Sprintf(format, a...)}
}

func unavailable(format string, a ...any) error {
	return &UnavailableError{Reason: fmt.Sprintf(format, a...)}
}

// IsUnavailable reports whether err means the store could not answer.
func IsUnavailable(err error) bool {
	var u *UnavailableError
	return errors.As(err, &u)
}

// Resolver holds the stores configured on a runner and who may read them.
type Resolver struct {
	stores map[string]entry
}

type entry struct {
	store    Store
	projects []string // glob patterns; empty allows every project
	keys     []string // glob patterns; empty allows every key
}

// NewResolver returns a resolver with only the default store, env, which
// reads the runner's own environment variables.
func NewResolver() *Resolver {
	return &Resolver{stores: map[string]entry{}}
}

// AddLocalEnv adds an env store called "env" unless one is defined. It is
// for running a pipeline on your own machine, where the environment is
// yours. A server or shared runner must not do this: it would let any
// pipeline read any variable in the runner's environment.
func (r *Resolver) AddLocalEnv() *Resolver {
	if _, ok := r.stores["env"]; !ok {
		r.Add("env", Env{}, nil, nil)
	}
	return r
}

// Add makes s available under name, replacing any store of that name. Only
// jobs whose project matches one of projects, asking for a key that matches
// one of keys, may read it; an empty list allows everything. Patterns are
// path.Match globs, so * does not match a slash.
func (r *Resolver) Add(name string, s Store, projects, keys []string) {
	r.stores[name] = entry{store: s, projects: projects, keys: keys}
}

// Lookup returns the value a reference points to, for a job of the given
// project. The store's allow-lists are checked before the store is asked.
func (r *Resolver) Lookup(ctx context.Context, project string, ref pipeline.SecretRef) (string, error) {
	e, ok := r.stores[ref.Store]
	if !ok {
		return "", notFound("this runner has no secret store called %q", ref.Store)
	}
	if !matchAny(e.projects, project) {
		return "", notFound("store %q does not allow project %q (projects: %s)", ref.Store, project, strings.Join(e.projects, ", "))
	}
	if !matchAny(e.keys, ref.Key) {
		return "", notFound("store %q does not allow key %q (keys: %s)", ref.Store, ref.Key, strings.Join(e.keys, ", "))
	}
	return e.store.Get(ctx, ref.Key, ref.Field)
}

func matchAny(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, value); ok {
			return true
		}
	}
	return false
}
