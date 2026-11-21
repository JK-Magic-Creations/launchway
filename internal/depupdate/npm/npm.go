// Package npm reads package.json and package-lock.json. pnpm and Yarn lock
// files are recognized and reported as not yet updated.
package npm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

// Parser reads package.json files.
type Parser struct{}

// Ecosystem is "npm".
func (Parser) Ecosystem() string { return depupdate.NPM }

// Detect matches package.json.
func (Parser) Detect(p string) bool { return depupdate.Base(p) == "package.json" }

type manifest struct {
	Name                 string            `json:"name"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PackageManager       string            `json:"packageManager"`
}

type lockFile struct {
	LockfileVersion int                  `json:"lockfileVersion"`
	Packages        map[string]lockEntry `json:"packages"`
	Dependencies    map[string]lockV1    `json:"dependencies"`
}

type lockEntry struct {
	Version string `json:"version"`
	Dev     bool   `json:"dev"`
	Link    bool   `json:"link"`
}

type lockV1 struct {
	Version string `json:"version"`
	Dev     bool   `json:"dev"`
}

// Parse lists the direct dependencies of a package.json with the version the
// lock file resolved, and the transitive ones the lock file records.
func (Parser) Parse(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
	var m manifest
	if err := json.Unmarshal(f.Data, &m); err != nil {
		return nil, nil, fmt.Errorf("package.json is not valid JSON: %w", err)
	}
	var notes []depupdate.Note
	dir := depupdate.Dir(f.Path)
	lockPath, lock, hasLock := "", lockFile{}, false
	if b, ok := f.Sibling("package-lock.json"); ok {
		if err := json.Unmarshal(b, &lock); err == nil {
			hasLock, lockPath = true, join(dir, "package-lock.json")
		} else {
			notes = append(notes, depupdate.Note{File: join(dir, "package-lock.json"), Message: "package-lock.json is not valid JSON; versions are read from package.json only"})
		}
	}
	for _, n := range []string{"pnpm-lock.yaml", "yarn.lock"} {
		if _, ok := f.Sibling(n); ok {
			notes = append(notes, depupdate.Note{File: join(dir, n), Message: n + " found: detected, but updates for this package manager are not available yet"})
		}
	}
	lines := keyLines(string(f.Data))
	var deps []depupdate.Dep
	add := func(set map[string]string, group string) {
		names := make([]string, 0, len(set))
		for n := range set {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			d := depupdate.Dep{Ecosystem: depupdate.NPM, Name: n, Constraint: set[n], Direct: true, Group: group, File: f.Path, Line: lines[n], LockFile: lockPath}
			d.Current = resolved(lock, hasLock, n)
			deps = append(deps, d)
		}
	}
	add(m.Dependencies, depupdate.GroupProd)
	add(m.DevDependencies, depupdate.GroupDev)
	add(m.OptionalDependencies, depupdate.GroupOpt)
	direct := map[string]bool{}
	for _, d := range deps {
		direct[d.Name] = true
	}
	if hasLock {
		deps = append(deps, transitive(lock, lockPath, f.Path, direct)...)
	}
	return deps, notes, nil
}

func resolved(l lockFile, has bool, name string) string {
	if !has {
		return ""
	}
	if e, ok := l.Packages["node_modules/"+name]; ok {
		return e.Version
	}
	return l.Dependencies[name].Version
}

func transitive(l lockFile, lockPath, manifestPath string, direct map[string]bool) []depupdate.Dep {
	var out []depupdate.Dep
	for k, e := range l.Packages {
		if !strings.HasPrefix(k, "node_modules/") || e.Link || e.Version == "" {
			continue
		}
		name := k[strings.LastIndex(k, "node_modules/")+len("node_modules/"):]
		if strings.Count(k, "node_modules/") == 1 && direct[name] {
			continue
		}
		g := depupdate.GroupProd
		if e.Dev {
			g = depupdate.GroupDev
		}
		out = append(out, depupdate.Dep{Ecosystem: depupdate.NPM, Name: name, Current: e.Version, Group: g, File: lockPath, LockFile: lockPath})
	}
	if len(l.Packages) == 0 {
		for name, e := range l.Dependencies {
			if direct[name] {
				continue
			}
			g := depupdate.GroupProd
			if e.Dev {
				g = depupdate.GroupDev
			}
			out = append(out, depupdate.Dep{Ecosystem: depupdate.NPM, Name: name, Current: e.Version, Group: g, File: lockPath, LockFile: lockPath})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Current < out[j].Current
	})
	return out
}

// keyLines maps a quoted key at the start of a JSON line to its line number.
func keyLines(src string) map[string]int {
	out := map[string]int{}
	for i, l := range strings.Split(src, "\n") {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, `"`) {
			continue
		}
		end := strings.Index(t[1:], `"`)
		if end < 0 || !strings.HasPrefix(strings.TrimSpace(t[end+2:]), ":") {
			continue
		}
		k := t[1 : end+1]
		if _, seen := out[k]; !seen {
			out[k] = i + 1
		}
	}
	return out
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
