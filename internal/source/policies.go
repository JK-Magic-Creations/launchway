package source

// Git sources of policies. A source is one of the server's libraries,
// pinned to a tag or a full commit id, and a directory of policy files in
// it. Every server loads each source on its own (the library's mirror is
// fetched as for templates) and stores what it loaded in the database, from
// which every server judges; see engine.StorePolicySource. Changing the
// policies is a reviewed commit and a new tag, like a release of a library.

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/policy"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// Limits on a source's files.
const (
	maxPolicyFiles     = 100
	maxPolicyFileBytes = 256 << 10
)

// PolicyLoad is the outcome of loading a source once.
type PolicyLoad struct {
	Commit   string
	Policies []policy.Policy
	// Refused says why the content at the pinned version cannot be used
	// (a missing version, a branch, a malformed file): the source then fails
	// closed. Unreachable says the source could not be read at all: what was
	// loaded before stays in use for a while.
	Refused     error
	Unreachable error
}

// LoadPolicySource loads a source's policies at its pinned version.
func (m *Manager) LoadPolicySource(ctx context.Context, sc engine.PolicySourceConfig) PolicyLoad {
	lib := m.libraries[sc.Library]
	if lib == nil {
		return PolicyLoad{Refused: fmt.Errorf("library %q is not one of the server's libraries (%s)", sc.Library, m.libraryNames())}
	}
	sha, kind, err := lib.resolveVersion(ctx, sc.Version, 0)
	switch {
	case errors.Is(err, engine.ErrNotFound):
		return PolicyLoad{Refused: fmt.Errorf("the library %s has no tag or commit %q", sc.Library, sc.Version)}
	case err != nil:
		return PolicyLoad{Unreachable: errors.New(redact.URLs(err.Error()))}
	case kind == "branch":
		return PolicyLoad{Commit: sha, Refused: fmt.Errorf("%s is a branch of %s; pin policies to a tag or a full commit id, so that changing them is a release", sc.Version, sc.Library)}
	}
	out := PolicyLoad{Commit: sha}
	files, err := lib.policyFiles(ctx, sha, sc.Path)
	if err != nil {
		out.Refused = err
		return out
	}
	var all [][]policy.Policy
	var problems policy.Problems
	for _, f := range files {
		ps, err := policy.ParseFile(f.data, f.name, sc.Name)
		var pp policy.Problems
		switch {
		case errors.As(err, &pp):
			problems = append(problems, pp...)
		case err != nil:
			problems = append(problems, policy.Problem{File: f.name, Message: err.Error()})
		}
		for i := range ps {
			ps[i].Version = sha
		}
		all = append(all, ps)
	}
	if len(problems) == 0 {
		if err := policy.Check(all...); err != nil {
			errors.As(err, &problems)
		}
	}
	if len(problems) > 0 {
		out.Refused = problems
		return out
	}
	for _, ps := range all {
		out.Policies = append(out.Policies, ps...)
	}
	return out
}

type policyFile struct {
	name string
	data []byte
}

// policyFiles reads the *.yaml and *.yml files under dir at a commit.
func (lib *project) policyFiles(ctx context.Context, sha, dir string) ([]policyFile, error) {
	if !pipeline.IsCommitID(sha) {
		return nil, fmt.Errorf("%q is not a commit", sha)
	}
	dir = strings.Trim(path.Clean("/"+dir), "/")
	lib.mu.Lock()
	defer lib.mu.Unlock()
	list, err := gitOut(ctx, lib.dir, "ls-tree", "-r", "--name-only", "-z", sha, "--", dir+"/")
	if err != nil {
		return nil, errors.New(redact.URLs(err.Error()))
	}
	var names []string
	for _, n := range strings.Split(list, "\x00") {
		if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("there are no policy files (*.yaml) under %s/ at %s", dir, sha[:12])
	}
	if len(names) > maxPolicyFiles {
		return nil, fmt.Errorf("there are %d policy files under %s/; at most %d", len(names), dir, maxPolicyFiles)
	}
	var out []policyFile
	for _, n := range names {
		size, err := gitOut(ctx, lib.dir, "cat-file", "-s", sha+":"+n)
		if err != nil {
			return nil, errors.New(redact.URLs(err.Error()))
		}
		if sz, err := strconv.Atoi(strings.TrimSpace(size)); err != nil || sz > maxPolicyFileBytes {
			return nil, fmt.Errorf("%s is larger than %d KiB", n, maxPolicyFileBytes>>10)
		}
		data, err := gitOut(ctx, lib.dir, "show", sha+":"+n)
		if err != nil {
			return nil, errors.New(redact.URLs(err.Error()))
		}
		if len(data) > maxPolicyFileBytes {
			return nil, fmt.Errorf("%s is larger than %d KiB", n, maxPolicyFileBytes>>10)
		}
		out = append(out, policyFile{name: n, data: []byte(data)})
	}
	return out, nil
}

// RefreshPolicySource loads a source and stores the outcome.
func (m *Manager) RefreshPolicySource(ctx context.Context, sc engine.PolicySourceConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	l := m.LoadPolicySource(ctx, sc)
	if l.Unreachable != nil {
		m.log.Warn("could not reach a policy source; what was loaded before is used until it is older than stale_after",
			"source", sc.Name, "err", l.Unreachable)
		return m.eng.PolicySourceUnreachable(sc.Name, sc.Version, l.Unreachable.Error())
	}
	refused := ""
	if l.Refused != nil {
		refused = l.Refused.Error()
	}
	return m.eng.StorePolicySource(sc.Name, sc.Version, l.Commit, l.Policies, refused)
}

// WatchPolicySources keeps every configured source loaded, each at its
// refresh interval, until ctx ends.
func (m *Manager) WatchPolicySources(ctx context.Context) {
	for _, sc := range m.eng.PolicySources() {
		go func() {
			t := time.NewTicker(sc.Refresh)
			defer t.Stop()
			for {
				if err := m.RefreshPolicySource(ctx, sc); err != nil && ctx.Err() == nil {
					m.log.Error("storing a policy source", "source", sc.Name, "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
}
