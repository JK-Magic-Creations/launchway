package python

import (
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
)

// pyCollector gathers the dependencies of one pyproject.toml.
type pyCollector struct {
	file, lock string
	lockVers   map[string]string
	deps       []depupdate.Dep
}

func (c *pyCollector) requirement(spec, group string, line int) {
	r, ok := ParseRequirement(spec)
	if !ok || r.DirectURL || r.Name == "" {
		return
	}
	d := depupdate.Dep{Ecosystem: depupdate.Python, Name: r.Name, Current: r.Pinned, Constraint: r.Spec, Direct: true,
		Group: group, File: c.file, Line: line, LockFile: c.lock}
	if v, ok := c.lockVers[r.Name]; ok {
		d.Current = v
	}
	c.deps = append(c.deps, d)
}

// pair reads one key of the file; group is where its requirements belong ("" when the key holds none).
func (c *pyCollector) pair(p tomlPair) {
	group := ""
	switch {
	case p.Table == "project" && p.Key == "dependencies":
		group = depupdate.GroupProd
	case p.Table == "project.optional-dependencies":
		group = depupdate.GroupOpt
	case p.Table == "dependency-groups":
		group = depupdate.GroupDev
	case p.Table == "build-system" && p.Key == "requires":
		group = depupdate.GroupTool
	case strings.HasPrefix(p.Table, "tool.poetry") && strings.HasSuffix(p.Table, "dependencies"):
		c.poetry(p)
		return
	}
	if group == "" {
		return
	}
	for _, s := range tomlStrings(p.Value) {
		c.requirement(s, group, p.Line)
	}
}

func (c *pyCollector) poetry(p tomlPair) {
	if p.Key == "python" {
		return
	}
	group := depupdate.GroupProd
	if strings.Contains(p.Table, "dev") || strings.Contains(p.Table, ".group.") {
		group = depupdate.GroupDev
	}
	spec := tomlString(p.Value)
	if spec == "" {
		if inlineField(p.Value, "git") != "" || inlineField(p.Value, "path") != "" || inlineField(p.Value, "url") != "" {
			return
		}
		spec = inlineField(p.Value, "version")
	}
	if spec == "" {
		return
	}
	d := depupdate.Dep{Ecosystem: depupdate.Python, Name: normalize(p.Key), Constraint: spec, Direct: true,
		Group: group, File: c.file, Line: p.Line, LockFile: c.lock}
	if v, ok := c.lockVers[d.Name]; ok {
		d.Current = v
	}
	c.deps = append(c.deps, d)
}

func parsePyproject(f depupdate.File) ([]depupdate.Dep, []depupdate.Note, error) {
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
	c := &pyCollector{file: f.Path, lock: lock, lockVers: lockVers}
	for _, p := range readTOML(string(f.Data)) {
		c.pair(p)
	}
	return c.deps, nil, nil
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
