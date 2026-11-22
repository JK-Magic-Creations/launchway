package pipeline

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Workspace is the pipeline's hints about its workspace.
type Workspace struct {
	// Exclude lists paths, globs as for artifacts, that a runner which carries the workspace from step to step
	// does not carry: what a step makes there stays in that step's own container and later steps do not see
	// it. For large trees that later steps re-create or get from a cache (a dependency directory, a build
	// output). A cache's own paths are never carried either, and need not be listed.
	Exclude []string `json:"exclude,omitempty"`
}

const (
	maxWorkspaceExclude = 50
	maxExcludeLen       = 200
)

var workspaceKeys = []string{"exclude"}

// parseWorkspace reads workspace: { exclude: [...] }.
func (ps *parser) parseWorkspace(p *Pipeline, n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		ps.add(n, "workspace", "expected a mapping: workspace: { exclude: [node_modules] }")
		return
	}
	w := &Workspace{}
	ps.eachKey(n, "workspace", workspaceKeys, func(k, v *yaml.Node) {
		w.Exclude = ps.strList(v, "workspace.exclude")
		if len(w.Exclude) > maxWorkspaceExclude {
			ps.add(v, "workspace.exclude", "at most %d paths may be left out of the workspace", maxWorkspaceExclude)
		}
		for _, e := range w.Exclude {
			if why := excludeProblem(e); why != "" {
				ps.add(v, "workspace.exclude", "%q %s", e, why)
			}
		}
	})
	p.Workspace = w
}

// excludeProblem says why a path cannot be left out of the workspace, or "".
func excludeProblem(e string) string {
	switch {
	case strings.TrimSpace(e) == "":
		return "is empty"
	case len(e) > maxExcludeLen:
		return "is too long"
	case strings.HasPrefix(e, "/"):
		return "is absolute: give it relative to the repository root"
	case strings.HasPrefix(e, "!"):
		return "starts with !: workspace.exclude only lists what to leave out"
	case strings.ContainsAny(e, "\\\x00\n"):
		return "has a character a path may not"
	case e == "." || e == "*" || e == "**" || e == "./":
		return "would leave out the whole workspace"
	}
	for _, seg := range strings.Split(strings.TrimSuffix(e, "/"), "/") {
		if seg == ".." || seg == "" || seg == "." {
			return "has a . or .. part or a doubled /: give a clean path below the repository root"
		}
	}
	return ""
}
