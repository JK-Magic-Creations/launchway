// Package workflow reads the "uses:" lines of workflow files that a
// repository still carries for its code host (.github/workflows), so such a
// repository is covered too. Each reference is an action (owner/repo) at a
// tag or at a commit with the version in a trailing comment.
package workflow

import (
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

// Parser reads workflow files.
type Parser struct{}

// Ecosystem is "workflow".
func (Parser) Ecosystem() string { return depupdate.Workflow }

// Detect matches .github/workflows/*.yml and *.yaml.
func (Parser) Detect(p string) bool {
	if !strings.Contains(p, ".github/workflows/") {
		return false
	}
	return strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml")
}

var usesRe = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*['"]?([^\s'"#@]+)@([^\s'"#]+)['"]?\s*(?:#\s*(\S+))?`)
var shaRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Parse lists the pinned actions. Local actions (./path) and docker://
// references are skipped; a reusable workflow (owner/repo/.github/workflows/x.yml)
// is listed under its repository.
func (Parser) Parse(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
	var deps []depupdate.Dep
	for i, l := range strings.Split(string(f.Data), "\n") {
		m := usesRe.FindStringSubmatch(l)
		if m == nil || strings.HasPrefix(m[1], ".") || strings.HasPrefix(m[1], "docker:") {
			continue
		}
		name := m[1]
		if parts := strings.Split(name, "/"); len(parts) > 2 {
			name = strings.Join(parts[:2], "/")
		}
		d := depupdate.Dep{Ecosystem: depupdate.Workflow, Name: name, Constraint: m[2], Current: m[2], Direct: true,
			Group: depupdate.GroupTool, File: f.Path, Line: i + 1}
		if shaRe.MatchString(m[2]) {
			d.Digest = m[2]
			d.Current = m[3] // the version the comment names; empty when it has none
		}
		deps = append(deps, d)
	}
	return deps, nil, nil
}

// Edit replaces an action reference on a line with a new tag or commit (and
// its comment version when the old line had one).
func Edit(src string, line int, oldRef, newRef, newComment string) (string, bool) {
	lines := strings.Split(src, "\n")
	if line < 1 || line > len(lines) || !strings.Contains(lines[line-1], "@"+oldRef) {
		return src, false
	}
	l := strings.Replace(lines[line-1], "@"+oldRef, "@"+newRef, 1)
	if i := strings.Index(l, "#"); i >= 0 && newComment != "" {
		l = strings.TrimRight(l[:i], " ") + " # " + newComment
	}
	lines[line-1] = l
	return strings.Join(lines, "\n"), true
}
