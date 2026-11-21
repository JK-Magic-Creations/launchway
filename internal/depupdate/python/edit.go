package python

import (
	"fmt"
	"regexp"
	"strings"
)

// Command is the toolchain step that regenerates a lock for an edited manifest.
type Command struct {
	Dir  string
	Args [][]string
}

// LockCommand names how a lock file is regenerated: uv.lock with
// "uv lock --upgrade-package name", poetry.lock with "poetry update name
// --lock", pinned requirements with hashes with "pip-compile
// --generate-hashes" (a pipeline step; the proposal says so in plain words).
func LockCommand(dir, lockFile, name string) (Command, bool) {
	base := lockFile
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	switch base {
	case "uv.lock":
		return Command{dir, [][]string{{"uv", "lock", "--upgrade-package", name}}}, true
	case "poetry.lock":
		return Command{dir, [][]string{{"poetry", "update", name, "--lock", "--no-interaction"}}}, true
	}
	return Command{}, false
}

// EditRequirements changes the pinned version of one package in a
// requirements file. When the file carries hashes the old hashes would no
// longer match: the hash lines of that requirement are removed and relock is
// true, meaning the pipeline must regenerate them; the pull request says so.
func EditRequirements(src, name, to string) (out string, relock bool, err error) {
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		r, ok := ParseRequirement(strings.TrimSuffix(strings.TrimSpace(stripComment(l)), "\\"))
		if !ok || r.Name != name || r.Pinned == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)(==\s*)` + regexp.QuoteMeta(r.Pinned))
		lines[i] = re.ReplaceAllString(lines[i], "${1}"+to)
		j := i
		for strings.HasSuffix(strings.TrimSpace(lines[j]), "\\") && j+1 < len(lines) && strings.Contains(lines[j+1], "--hash=") {
			relock = true
			j++
		}
		if relock {
			lines[i] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(lines[i]), "\\"))
			lines = append(lines[:i+1], lines[j+1:]...)
		}
		return strings.Join(lines, "\n"), relock, nil
	}
	return src, false, fmt.Errorf("%s is not pinned with == in this file", name)
}

// EditPyproject changes the version written after the package's name in a
// pyproject.toml requirement string or Poetry table.
func EditPyproject(src, name, to string) (string, error) {
	re := regexp.MustCompile(`(?im)(["']` + strings.ReplaceAll(regexp.QuoteMeta(name), `-`, `[-_.]`) + `\s*(?:\[[^\]]*\])?\s*(?:==|>=|~=|\^|~)\s*)([0-9][^"',;\s]*)`)
	if loc := re.FindStringSubmatchIndex(src); loc != nil {
		return src[:loc[4]] + to + src[loc[5]:], nil
	}
	re2 := regexp.MustCompile(`(?im)^(\s*` + strings.ReplaceAll(regexp.QuoteMeta(name), `-`, `[-_.]`) + `\s*=\s*(?:\{[^}]*version\s*=\s*)?["'](?:\^|~|>=|==)?)([0-9][^"']*)`)
	if loc := re2.FindStringSubmatchIndex(src); loc != nil {
		return src[:loc[4]] + to + src[loc[5]:], nil
	}
	return src, fmt.Errorf("the requirement for %s could not be found to edit", name)
}
