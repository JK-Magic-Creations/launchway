// Package container reads the container images a repository names: the FROM
// lines of Dockerfiles (multi-stage, with ARG defaults), the "image:" of
// Launchway pipeline files, and shell variables holding an image reference in
// files the repository's settings name.
package container

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

// Parser reads image references. Extra is a list of file globs (matched on
// the repository-relative path) whose lines of the form NAME_IMAGE='ref' are
// read too; it comes from the repository's update settings.
type Parser struct{ Extra []string }

// Ecosystem is "container".
func (Parser) Ecosystem() string { return depupdate.Container }

// Detect matches Dockerfiles, Containerfiles, pipeline files and Extra globs.
func (p Parser) Detect(f string) bool {
	b := depupdate.Base(f)
	lb := strings.ToLower(b)
	switch {
	case lb == "dockerfile" || lb == "containerfile" || strings.HasPrefix(lb, "dockerfile.") || strings.HasSuffix(lb, ".dockerfile"):
		return true
	case isPipeline(f):
		return true
	}
	for _, g := range p.Extra {
		if ok, _ := path.Match(g, f); ok {
			return true
		}
	}
	return false
}

func isPipeline(f string) bool {
	if !strings.HasPrefix(f, ".launchway/") && !strings.Contains(f, "/.launchway/") {
		return false
	}
	b := depupdate.Base(f)
	return (strings.HasSuffix(b, ".yaml") || strings.HasSuffix(b, ".yml")) && b != "updates.yaml" && b != "updates.yml"
}

// Ref is an image reference taken apart.
type Ref struct {
	Repo   string // registry/path without tag or digest
	Tag    string
	Digest string
}

// ParseRef splits "registry:5000/a/b:tag@sha256:..." into repo, tag, digest.
func ParseRef(s string) Ref {
	r := Ref{}
	if i := strings.Index(s, "@"); i >= 0 {
		r.Digest, s = s[i+1:], s[:i]
	}
	slash := strings.LastIndex(s, "/")
	if c := strings.LastIndex(s, ":"); c > slash {
		r.Tag, s = s[c+1:], s[:c]
	}
	r.Repo = s
	return r
}

// String is the reference again.
func (r Ref) String() string {
	s := r.Repo
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

var (
	argRe   = regexp.MustCompile(`^(?i)ARG\s+([A-Za-z_][A-Za-z0-9_]*)(?:=(.*))?$`)
	fromRe  = regexp.MustCompile(`^(?i)FROM\s+(?:--[a-z-]+=\S+\s+)*(\S+)(?:\s+AS\s+(\S+))?`)
	varRe   = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	pipeRe  = regexp.MustCompile(`^\s*(?:-\s+)?image:\s*['"]?([^\s'"#]+)['"]?\s*(?:#.*)?$`)
	shellRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Z][A-Z0-9_]*(?:IMAGE|_REF))=['"]?([^\s'"#]+)['"]?`)
)

// Parse reads one file.
func (p Parser) Parse(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
	text := string(f.Data)
	b := strings.ToLower(depupdate.Base(f.Path))
	switch {
	case isPipeline(f.Path):
		return scan(f, text, pipeRe, depupdate.GroupTool, nil)
	case b == "dockerfile" || b == "containerfile" || strings.HasPrefix(b, "dockerfile.") || strings.HasSuffix(b, ".dockerfile"):
		return parseDockerfile(f, text)
	}
	return scan(f, text, shellRe, depupdate.GroupTool, func(m []string) string { return m[2] })
}

func scan(f depupdate.File, text string, re *regexp.Regexp, group string, pick func([]string) string) ([]depupdate.Dep, []depupdate.Note, error) {
	var deps []depupdate.Dep
	for i, l := range strings.Split(text, "\n") {
		m := re.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ref := m[len(m)-1]
		if pick != nil {
			ref = pick(m)
		}
		if d, ok := dep(ref, f.Path, i+1, group); ok {
			deps = append(deps, d)
		}
	}
	return deps, nil, nil
}

func dep(ref, file string, line int, group string) (depupdate.Dep, bool) {
	if ref == "" || ref == "scratch" || strings.ContainsAny(ref, "${}") {
		return depupdate.Dep{}, false
	}
	r := ParseRef(ref)
	if r.Repo == "" || !strings.ContainsAny(r.Repo, "abcdefghijklmnopqrstuvwxyz") {
		return depupdate.Dep{}, false
	}
	cur := r.Tag
	if cur == "" && r.Digest == "" {
		cur = "latest"
	}
	return depupdate.Dep{Ecosystem: depupdate.Container, Name: r.Repo, Current: cur, Constraint: r.Tag, Digest: r.Digest,
		Direct: true, Group: group, File: file, Line: line}, true
}

func parseDockerfile(f depupdate.File, text string) ([]depupdate.Dep, []depupdate.Note, error) {
	var deps []depupdate.Dep
	args := map[string]string{}
	argLine := map[string]int{}
	stages := map[string]bool{}
	seenFrom := false
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		for strings.HasSuffix(l, "\\") && i+1 < len(lines) {
			i++
			l = strings.TrimSpace(strings.TrimSuffix(l, "\\")) + " " + strings.TrimSpace(lines[i])
		}
		if m := argRe.FindStringSubmatch(l); m != nil && !seenFrom {
			args[m[1]] = strings.Trim(strings.TrimSpace(m[2]), `"'`)
			argLine[m[1]] = i + 1
			continue
		}
		m := fromRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		seenFrom = true
		ref, line := m[1], i+1
		if k := argOf(ref); k != "" {
			if v, ok := args[k]; ok && v != "" {
				ref, line = v, argLine[k]
			}
		}
		ref = varRe.ReplaceAllStringFunc(ref, func(s string) string {
			sm := varRe.FindStringSubmatch(s)
			name := sm[1]
			if name == "" {
				name = sm[3]
			}
			if v, ok := args[name]; ok && v != "" {
				return v
			}
			return sm[2]
		})
		if stages[ref] {
			if m[2] != "" {
				stages[m[2]] = true
			}
			continue
		}
		if m[2] != "" {
			stages[m[2]] = true
		}
		if d, ok := dep(ref, f.Path, line, depupdate.GroupProd); ok {
			// a ref used by two FROMs is listed once, at its definition
			dup := false
			for _, e := range deps {
				if e.Line == d.Line && e.Name == d.Name {
					dup = true
				}
			}
			if !dup {
				deps = append(deps, d)
			}
		}
	}
	return deps, nil, nil
}

// argOf returns the name when the whole reference is "${NAME}" or "$NAME".
func argOf(ref string) string {
	m := varRe.FindStringSubmatch(ref)
	if m == nil || m[0] != ref {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[3]
}

// Edit replaces an image reference on a given line (1-based) with a new one.
// Only that line changes; a tag or digest is replaced, never reformatted.
func Edit(src string, line int, oldRef, newRef string) (string, error) {
	lines := strings.Split(src, "\n")
	if line < 1 || line > len(lines) || !strings.Contains(lines[line-1], oldRef) {
		return src, errNotFound(oldRef, line)
	}
	lines[line-1] = strings.Replace(lines[line-1], oldRef, newRef, 1)
	return strings.Join(lines, "\n"), nil
}

func errNotFound(ref string, line int) error {
	return fmt.Errorf("the image %s is not on line %d any more", ref, line)
}
