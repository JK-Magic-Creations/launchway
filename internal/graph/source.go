package graph

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// A source is a small adapter that turns one kind of data into graph facts.
// It reads one scope and returns every fact it asserts for it; the store does
// the comparing, so a source never has to know what the graph held before.
// Other work plugs in by implementing Source and adding it to a Runner: the
// built-in registry, VM and image delivery, configuration as code and
// repository browsing each bring their own.

// Scope names what a source is asked to read.
type Scope struct {
	Kind string // ScopeRepository, ScopeProject or ScopeInstallation
	Ref  string // the repository path, the project name, or ""
}

// The kinds of scope.
const (
	ScopeRepository   = "repository"
	ScopeProject      = "project"
	ScopeInstallation = "installation"
)

// Key is the scope as the store keeps it.
func (s Scope) Key() string {
	if s.Ref == "" {
		return s.Kind
	}
	return s.Kind + ":" + s.Ref
}

// Source reads one kind of data.
type Source interface {
	// Name is the stable name facts carry as their origin, for example
	// "manifests". It is how a fact says who asserted it.
	Name() string
	// Scopes says which kind of scope the source reads.
	Scopes() []string
	// Assert reads the scope and returns everything the source asserts about
	// it, and the commit or moment it read (kept as the facts' ref).
	Assert(ctx context.Context, sc Scope) (Assertion, string, error)
}

// Repo is a hosted repository as the sources see it.
type Repo struct {
	ID            string
	Path          string // owner/name
	DefaultBranch string
	Archived      bool
}

// Repos is what the repository sources need of Git hosting.
type Repos interface {
	// List returns every hosted repository.
	List(ctx context.Context) ([]Repo, error)
	// Files returns the files of the repository's default branch that may say
	// something (manifests, Containerfiles, pipeline files, the requirements
	// file, and files matching the extra globs) and the commit they were read
	// at. A repository with no commits returns an empty tree and "".
	Files(ctx context.Context, r Repo, extra []string) (fs.FS, string, error)
}

// Config is what the sources share.
type Config struct {
	// RegistryHosts maps the registry names pipelines use to the hosts
	// image references carry.
	RegistryHosts map[string]string
	// Projects maps a project to the hosted repository it builds (by path).
	ProjectRepos func(ctx context.Context) (map[string]string, error)
}

// Runner keeps the graph current: it runs sources over scopes and applies
// what they say.
type Runner struct {
	Store   *Store
	Repos   Repos
	Sources []Source
	// Projects lists project names for project scopes.
	Projects func(ctx context.Context) ([]string, error)

	mu sync.Mutex
}

// RunResult is what an update did.
type RunResult struct {
	Scope   string                 `json:"scope"`
	Sources map[string]ApplyResult `json:"sources"`
	Errors  map[string]string      `json:"errors,omitempty"`
}

// Update reads one scope with every source that reads that kind of scope.
// A source that fails leaves what the graph holds for it as it was (a
// failed read never closes edges) and is reported.
func (r *Runner) Update(ctx context.Context, sc Scope) (RunResult, error) {
	res := RunResult{Scope: sc.Key(), Sources: map[string]ApplyResult{}, Errors: map[string]string{}}
	for _, src := range r.Sources {
		if !contains(src.Scopes(), sc.Kind) {
			continue
		}
		a, ref, err := src.Assert(ctx, sc)
		if err != nil {
			res.Errors[src.Name()] = err.Error()
			continue
		}
		ar, err := r.Store.Apply(ctx, src.Name(), sc.Key(), ref, a)
		if err != nil {
			res.Errors[src.Name()] = err.Error()
			continue
		}
		res.Sources[src.Name()] = ar
	}
	if len(res.Errors) == 0 {
		res.Errors = nil
	}
	return res, nil
}

// PushedRepository is the incremental update after a push: the repository's
// own sources run again, and so do the sources that read the installation's
// deployments, so a change shows at once.
func (r *Runner) PushedRepository(ctx context.Context, path string) (RunResult, error) {
	return r.Update(ctx, Scope{ScopeRepository, path})
}

// Rescan reads every scope again: every repository, every project, and the
// installation. It is what a rebuild does after clearing the graph.
func (r *Runner) Rescan(ctx context.Context) ([]RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []RunResult
	if r.Repos != nil {
		repos, err := r.Repos.List(ctx)
		if err != nil {
			return nil, err
		}
		sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
		for _, rp := range repos {
			u, err := r.Update(ctx, Scope{ScopeRepository, rp.Path})
			if err != nil {
				return out, err
			}
			out = append(out, u)
		}
	}
	if r.Projects != nil {
		ps, err := r.Projects(ctx)
		if err != nil {
			return out, err
		}
		sort.Strings(ps)
		for _, p := range ps {
			u, err := r.Update(ctx, Scope{ScopeProject, p})
			if err != nil {
				return out, err
			}
			out = append(out, u)
		}
	}
	u, err := r.Update(ctx, Scope{ScopeInstallation, ""})
	if err != nil {
		return out, err
	}
	return append(out, u), nil
}

// Rebuild clears the graph and reads everything again. The user's
// overrides are kept. The result equals what incremental updates built.
func (r *Runner) Rebuild(ctx context.Context) ([]RunResult, error) {
	if err := r.Store.Clear(ctx); err != nil {
		return nil, err
	}
	return r.Rescan(ctx)
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

// readFiles is a helper for sources that read the repository's files.
func repoFiles(ctx context.Context, rs Repos, path string, extra []string) (fs.FS, string, Repo, error) {
	all, err := rs.List(ctx)
	if err != nil {
		return nil, "", Repo{}, err
	}
	for _, rp := range all {
		if rp.Path == path {
			f, c, err := rs.Files(ctx, rp, extra)
			return f, c, rp, err
		}
	}
	return nil, "", Repo{}, fmt.Errorf("the repository %s is not hosted here", path)
}

// ExtraGlobs are the files, besides manifests, that the repository sources
// ask Git hosting to read: Kubernetes manifests and the requirements file.
var ExtraGlobs = []string{"k8s/*.yaml", "k8s/*.yml", "deploy/*.yaml", "deploy/*.yml", "manifests/*.yaml", "manifests/*.yml", "kubernetes/*.yaml",
	"deploy/*/*.yaml", "k8s/*/*.yaml", "CODEOWNERS", ".github/CODEOWNERS", "docs/CODEOWNERS"}

func cleanPath(p string) string { return strings.TrimPrefix(strings.TrimPrefix(p, "./"), "/") }

// Remove forgets a scope that is gone (a repository deleted, a project
// removed): every source's facts about it are closed.
func (r *Runner) Remove(ctx context.Context, sc Scope) error {
	for _, src := range r.Sources {
		if !contains(src.Scopes(), sc.Kind) {
			continue
		}
		if _, err := r.Store.Apply(ctx, src.Name(), sc.Key(), "", Assertion{}); err != nil {
			return err
		}
	}
	return nil
}
