// Package python reads requirements*.txt, pyproject.toml (standard project
// tables, dependency groups and Poetry's tables), uv.lock and poetry.lock.
package python

import (
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

// Parser reads Python manifests.
type Parser struct{}

// Ecosystem is "python".
func (Parser) Ecosystem() string { return depupdate.Python }

var reqName = regexp.MustCompile(`^(?i)(requirements|constraints)([-_.][A-Za-z0-9_.-]+)?\.(txt|in)$|^(?i)requirements\.txt$`)

// Detect matches requirements*.txt, requirements*.in and pyproject.toml.
func (Parser) Detect(p string) bool {
	b := depupdate.Base(p)
	return b == "pyproject.toml" || reqName.MatchString(b)
}

// Parse dispatches on the file kind.
func (Parser) Parse(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
	if depupdate.Base(f.Path) == "pyproject.toml" {
		return parsePyproject(f)
	}
	return parseRequirements(f)
}

// Requirement is one parsed requirement line.
type Requirement struct {
	Name      string
	Extras    string
	Spec      string // ">=1,<2" or "==1.2.3"
	Pinned    string // the version when the spec is a single ==
	Marker    string
	HasHash   bool
	DirectURL bool
}

var reqRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[^\]]*\])?\s*([^;@]*?)\s*(?:;\s*(.*))?$`)

// ParseRequirement reads "name[extra]==1.0 ; marker" (without any options).
func ParseRequirement(s string) (Requirement, bool) {
	s = strings.TrimSpace(s)
	r := Requirement{}
	if strings.Contains(s, " @ ") || strings.Contains(s, "@git+") || strings.HasPrefix(s, "git+") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		r.DirectURL = true
	}
	head := s
	if i := strings.Index(head, " @ "); i > 0 {
		head = head[:i]
	}
	m := reqRe.FindStringSubmatch(strings.TrimSpace(head))
	if m == nil {
		return r, false
	}
	r.Name = normalize(m[1])
	r.Extras = m[2]
	r.Spec = strings.ReplaceAll(strings.TrimSpace(strings.Trim(m[3], "()")), " ", "")
	r.Marker = m[4]
	if strings.HasPrefix(r.Spec, "==") && !strings.ContainsAny(r.Spec, ",*") {
		r.Pinned = strings.TrimPrefix(r.Spec, "==")
	}
	return r, true
}

var sepRun = regexp.MustCompile(`[-_.]+`)

func normalize(n string) string { return strings.ToLower(sepRun.ReplaceAllString(n, "-")) }

func parseRequirements(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
	var deps []depupdate.Dep
	var notes []depupdate.Note
	lines := strings.Split(string(f.Data), "\n")
	group := depupdate.GroupProd
	if strings.Contains(strings.ToLower(depupdate.Base(f.Path)), "dev") || strings.Contains(strings.ToLower(depupdate.Base(f.Path)), "test") {
		group = depupdate.GroupDev
	}
	for i := 0; i < len(lines); i++ {
		start := i + 1
		l := strings.TrimSpace(stripComment(lines[i]))
		hash := false
		for strings.HasSuffix(l, "\\") && i+1 < len(lines) {
			i++
			next := strings.TrimSpace(stripComment(lines[i]))
			if strings.Contains(next, "--hash=") {
				hash = true
			}
			l = strings.TrimSpace(strings.TrimSuffix(l, "\\")) + " " + next
		}
		if strings.Contains(l, "--hash=") {
			hash = true
		}
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "-") {
			if strings.HasPrefix(l, "-r ") || strings.HasPrefix(l, "-c ") || strings.HasPrefix(l, "--requirement") {
				continue
			}
			if !strings.HasPrefix(l, "--hash") {
				continue
			}
		}
		if k := strings.Index(l, " --"); k > 0 {
			l = l[:k]
		}
		r, ok := ParseRequirement(l)
		if !ok || r.Name == "" {
			continue
		}
		if r.DirectURL {
			notes = append(notes, depupdate.Note{File: f.Path, Message: r.Name + " is installed from an address and is not updated"})
			continue
		}
		d := depupdate.Dep{Ecosystem: depupdate.Python, Name: r.Name, Current: r.Pinned, Constraint: r.Spec,
			Direct: true, Group: group, File: f.Path, Line: start}
		if hash {
			d.LockFile = f.Path // the hashes in this very file are the lock
		}
		deps = append(deps, d)
	}
	return deps, notes, nil
}

func stripComment(s string) string {
	if i := strings.Index(s, " #"); i >= 0 {
		return s[:i]
	}
	if strings.HasPrefix(strings.TrimSpace(s), "#") {
		return ""
	}
	return s
}

// HasHashes reports whether a requirements file pins hashes.
func HasHashes(src string) bool { return strings.Contains(src, "--hash=") }
