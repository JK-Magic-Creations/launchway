package standards

import (
	"path"
	"strings"
)

// matches reports whether the file (a repository-relative path, slash
// separated) matches the matcher, and what to call what was found.
func (m Matcher) matches(p string) (string, bool) {
	base := path.Base(p)
	for _, n := range m.Names {
		if n == "*" || n == base {
			if m.Label != "" {
				return m.Label, true
			}
			return base, true
		}
	}
	hasSuffix := func() bool {
		for _, s := range m.Suffix {
			if strings.HasSuffix(base, s) {
				return true
			}
		}
		return false
	}
	switch {
	case len(m.Dirs) > 0:
		if len(m.Suffix) > 0 && !hasSuffix() {
			return "", false
		}
		for _, part := range strings.Split(path.Dir(p), "/") {
			for _, d := range m.Dirs {
				if part == d {
					return m.label(p), true
				}
			}
		}
	case m.Prefix != "" && len(m.Suffix) > 0:
		if strings.HasPrefix(base, m.Prefix) && hasSuffix() {
			return m.label(p), true
		}
	case m.Prefix != "":
		if strings.HasPrefix(base, m.Prefix) {
			return m.label(p), true
		}
	case len(m.Suffix) > 0:
		if hasSuffix() {
			return m.label(p), true
		}
	}
	return "", false
}

func (m Matcher) label(p string) string {
	if m.Label != "" {
		return m.Label
	}
	return path.Base(p)
}

// skippedPath says a file is not the repository's own: vendored and
// installed code and the scratch directory of this feature.
func skippedPath(p string) bool {
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "node_modules", "vendor", ".standing", ".git", "third_party", "testdata":
			return true
		}
	}
	return false
}

// Applies tells whether the check applies to a repository with these files,
// and says why in a clause such as "go.mod was found".
func (c Check) Applies(files []string) (string, bool) {
	for _, p := range files {
		if skippedPath(p) {
			continue
		}
		for _, m := range c.Detect {
			if what, ok := m.matches(p); ok {
				if what == "every repository" {
					return "it applies to every repository", true
				}
				return what + " was found", true
			}
		}
	}
	return "", false
}

// EcosystemMatchers say which files mean a repository has an ecosystem. It
// is the profile's own list; the dependency updater has another, for
// manifests only, and the two agree where they overlap (a test checks it).
var ecosystemFiles = map[string][]Matcher{
	Go:         {{Names: []string{"go.mod"}}},
	JavaScript: {{Names: []string{"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "tsconfig.json"}}},
	Python:     {{Names: []string{"pyproject.toml", "Pipfile", "setup.py", "poetry.lock", "uv.lock"}}, {Prefix: "requirements", Suffix: []string{".txt"}}, {Suffix: []string{".py"}}},
	Java:       {{Names: []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "gradle.lockfile"}}},
	Container:  dockerfiles,
	Kubernetes: {{Names: []string{"Chart.yaml", "kustomization.yaml"}}, {Dirs: []string{"k8s", "kubernetes", "manifests", "charts", "helm"}, Suffix: []string{".yaml", ".yml"}}},
	Infra:      {{Suffix: []string{".tf", ".tfvars"}}, {Dirs: []string{"ansible", "playbooks"}, Suffix: []string{".yaml", ".yml"}}},
	Shell:      {{Suffix: []string{".sh", ".bash"}}},
}

// Detected is an ecosystem found in a repository and the file that showed it.
type Detected struct {
	Ecosystem string `json:"ecosystem"`
	Title     string `json:"title"`
	Because   string `json:"because"`
}

// Detect lists the ecosystems a repository with these files has. "Any
// repository" is always first.
func Detect(files []string) []Detected {
	out := []Detected{{Any, EcosystemTitle[Any], "it applies to every repository"}}
	for _, eco := range Ecosystems[1:] {
	files:
		for _, p := range files {
			if skippedPath(p) {
				continue
			}
			for _, m := range ecosystemFiles[eco] {
				if what, ok := m.matches(p); ok {
					out = append(out, Detected{eco, EcosystemTitle[eco], what + " was found"})
					break files
				}
			}
		}
	}
	return out
}
