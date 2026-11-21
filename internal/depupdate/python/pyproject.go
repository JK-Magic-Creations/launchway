package python

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

func parsePyproject(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
	pairs := readTOML(string(f.Data))
	lockName, lockVers := "", map[string]string{}
	if b, ok := f.Sibling("uv.lock"); ok {
		lockName, lockVers = "uv.lock", readLock(string(b))
	} else if b, ok := f.Sibling("poetry.lock"); ok {
		lockName, lockVers = "poetry.lock", readLock(string(b))
	}
	lock := ""
	if lockName != "" {
		lock = join(depupdate.Dir(f.Path), lockName)
	}
	var deps []depupdate.Dep
	add := func(spec, group string, line int) {
		r, ok := ParseRequirement(spec)
		if !ok || r.DirectURL || r.Name == "" {
			return
		}
		d := depupdate.Dep{Ecosystem: depupdate.Python, Name: r.Name, Current: r.Pinned, Constraint: r.Spec, Direct: true,
			Group: group, File: f.Path, Line: line, LockFile: lock}
		if v, ok := lockVers[r.Name]; ok {
			d.Current = v
		}
		deps = append(deps, d)
	}
	for _, p := range pairs {
		switch {
		case p.Table == "project" && p.Key == "dependencies":
			for _, s := range tomlStrings(p.Value) {
				add(s, depupdate.GroupProd, p.Line)
			}
		case p.Table == "project" && p.Key == "requires-python":
		case p.Table == "project.optional-dependencies":
			for _, s := range tomlStrings(p.Value) {
				add(s, depupdate.GroupOpt, p.Line)
			}
		case p.Table == "dependency-groups":
			for _, s := range tomlStrings(p.Value) {
				add(s, depupdate.GroupDev, p.Line)
			}
		case p.Table == "build-system" && p.Key == "requires":
			for _, s := range tomlStrings(p.Value) {
				add(s, depupdate.GroupTool, p.Line)
			}
		case strings.HasPrefix(p.Table, "tool.poetry") && strings.HasSuffix(p.Table, "dependencies"):
			if p.Key == "python" {
				continue
			}
			group := depupdate.GroupProd
			if strings.Contains(p.Table, "dev") || strings.Contains(p.Table, ".group.") {
				group = depupdate.GroupDev
			}
			spec := tomlString(p.Value)
			if spec == "" {
				if inlineField(p.Value, "git") != "" || inlineField(p.Value, "path") != "" || inlineField(p.Value, "url") != "" {
					continue
				}
				spec = inlineField(p.Value, "version")
			}
			if spec == "" {
				continue
			}
			d := depupdate.Dep{Ecosystem: depupdate.Python, Name: normalize(p.Key), Constraint: spec, Direct: true,
				Group: group, File: f.Path, Line: p.Line, LockFile: lock}
			if v, ok := lockVers[d.Name]; ok {
				d.Current = v
			}
			deps = append(deps, d)
		}
	}
	return deps, nil, nil
}

// readLock reads name and version of every [[package]] in a uv.lock or
// poetry.lock.
func readLock(src string) map[string]string {
	names := map[int]string{}
	vers := map[int]string{}
	for _, p := range readTOML(src) {
		if p.Table != "package" || p.Index < 0 {
			continue
		}
		switch p.Key {
		case "name":
			names[p.Index] = normalize(tomlString(p.Value))
		case "version":
			vers[p.Index] = tomlString(p.Value)
		}
	}
	out := map[string]string{}
	for i, n := range names {
		out[n] = vers[i]
	}
	return out
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
