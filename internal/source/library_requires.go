package source

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
)

// manifestAt reads a library's library.yaml at a commit; nil if it has none.
func (lib *project) manifestAt(ctx context.Context, sha string) ([]byte, error) {
	if !pipeline.IsCommitID(sha) {
		return nil, nil
	}
	lib.mu.Lock()
	defer lib.mu.Unlock()
	data, err := gitOut(ctx, lib.dir, "show", sha+":"+pipeline.LibraryManifestPath)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "exists on disk, but not in") {
			return nil, nil
		}
		return nil, errors.New(redact.URLs(err.Error()))
	}
	return []byte(data), nil
}

// resolveRequires reads the library.yaml of a library at its commit and resolves what it requires, each at the
// version it pins, to commits, and what those require in turn, to the depth templates may be used. A pin that
// is a branch is refused, and so is a library that requires one on the way down (a circle). The result is set as
// parent.Requires; stack holds repository@commit of every library on the way down.
func (m *Manager) resolveRequires(ctx context.Context, parent *pipeline.Library, mirror *project, fresh time.Duration, depth int, stack []string) pipeline.Problems {
	data, err := mirror.manifestAt(ctx, parent.Commit)
	if err != nil {
		return pipeline.Problems{{File: pipeline.LibraryManifestPath, Library: parent.Name, Message: err.Error()}}
	}
	if data == nil {
		return nil
	}
	reqs, ps := pipeline.ParseLibraryManifest(data)
	var out pipeline.Problems
	for _, p := range ps {
		p.File, p.Library = pipeline.LibraryManifestPath, parent.Name
		out = append(out, p)
	}
	if depth >= pipeline.MaxNesting {
		return append(out, pipeline.Problem{File: pipeline.LibraryManifestPath, Library: parent.Name, Message: fmt.Sprintf(
			"libraries require each other %d deep from %s; at most %d levels are allowed", depth+1, parent.Name, pipeline.MaxNesting)})
	}
	for _, d := range pipeline.RequiredLibraries(parent.Name, reqs) {
		bad := func(format string, a ...any) {
			out = append(out, pipeline.Problem{File: pipeline.LibraryManifestPath, Library: parent.Name, Line: d.Line, Path: "requires." + d.Name,
				Message: fmt.Sprintf(format, a...)})
		}
		lib := m.c().libraries[d.Repo]
		if lib == nil {
			bad("%q is not a library this server allows (%s); an administrator lists libraries in the server's configuration", d.Repo, m.c().libraryNames())
			continue
		}
		sha, kind, err := lib.resolveVersion(ctx, d.Version, fresh)
		switch {
		case errors.Is(err, engine.ErrNotFound):
			bad("the library %s (%s) has no tag or commit called %q", d.Name, d.Repo, d.Version)
			continue
		case err != nil:
			bad("reading the library %s (%s): %s", d.Name, d.Repo, redact.URLs(err.Error()))
			continue
		case kind == "branch":
			bad("%q is a branch of %s, which moves; pin a tag or a full commit id", d.Version, d.Repo)
			continue
		}
		d.Commit, d.RefKind = sha, kind
		key := d.Repo + "@" + sha
		if slices.Contains(stack, key) {
			bad("libraries require each other in a circle: %s requires %s again", parent.Name, d.Name)
			continue
		}
		out = append(out, m.resolveRequires(ctx, d, lib, fresh, depth+1, append(slices.Clone(stack), key))...)
		parent.Requires = append(parent.Requires, d)
	}
	return out
}
