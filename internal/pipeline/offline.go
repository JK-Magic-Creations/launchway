package pipeline

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/starters"
	"github.com/jk-magic-creations/launchway/internal/stepkit"
)

// Offline expansion: the templates of a pipeline read from library
// directories on disk, with no server. It runs the same steps a server runs
// when it checks a pipeline for a person typing it: parse, bind with preview
// values (Preview), set each library's commit, expand (ExpandWith). The
// differences are only where a template comes from (a directory, not a
// repository at a commit) and that no server configuration is consulted:
// which libraries and environments a server allows, and its limits, are the
// server's to check.

// Preview binds a pipeline to the values a preview shows (its inputs'
// defaults, loops whose values are not known standing aside). A server
// checking text and the command line both call it.
func Preview(p *Pipeline, o BindOptions) (*Pipeline, error) {
	o.Preview = true
	return Bind(p, PreviewValues(p), o)
}

// OfflineCommit is the commit an offline expansion records for a library
// read from a directory.
const OfflineCommit = "0000000000000000000000000000000000000000"

// ParseLibraryFlags reads NAME=PATH arguments (the --library flag).
func ParseLibraryFlags(args []string) (map[string]string, error) {
	out := map[string]string{}
	for _, a := range args {
		name, dir, ok := strings.Cut(a, "=")
		switch {
		case !ok || name == "" || dir == "":
			return nil, fmt.Errorf("--library %q: write NAME=DIRECTORY, where NAME is what the pipeline's libraries section calls the library", a)
		case !ValidLibraryName(name):
			return nil, fmt.Errorf("--library %q: %q is not a library name (lower-case letters, digits and hyphens)", a, name)
		case out[name] != "":
			return nil, fmt.Errorf("--library %s is given twice", name)
		}
		out[name] = dir
	}
	return out, nil
}

// ExpandOffline parses a pipeline and expands its templates from library
// directories, by the name the pipeline gives each library. A pipeline that
// uses no templates is returned as parsed. Problems are the ones a server
// reports, with one more that only an offline check can make: a library the
// pipeline names has no directory.
func ExpandOffline(data []byte, dirs map[string]string, o BindOptions, vars Variables) (*Pipeline, error) {
	p, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if p, err = Preview(p, o); err != nil {
		return nil, err
	}
	selfDir := dirs[SelfLibrary]
	if len(p.Libraries) == 0 && selfDir == "" && !p.usesSelf() {
		return p, nil
	}
	var problems Problems
	for _, l := range p.Libraries {
		dir, ok := dirs[l.Name]
		if !ok {
			problems = append(problems, Problem{Line: l.Line, Path: "libraries." + l.Name,
				Message: fmt.Sprintf("the pipeline uses the library %s (%s, version %s); give its directory with --library %s=DIRECTORY%s",
					l.Name, l.Repo, l.Wanted(), l.Name, knownDirs(dirs))})
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, templatesDir)); (err != nil || !st.IsDir()) && !isDir(filepath.Join(dir, stepkit.StepDir)) {
			problems = append(problems, Problem{Line: l.Line, Path: "libraries." + l.Name,
				Message: fmt.Sprintf("%s has no %s directory: --library %s=DIRECTORY takes the root of a library, the directory that holds templates/", dir, strings.TrimSuffix(templatesDir, "/"), l.Name)})
			continue
		}
		l.Commit, l.RefKind, l.Dir = OfflineCommit, "directory", dir
		problems = append(problems, requireOffline(l, dirs, 0, []string{dir})...)
	}
	for name := range dirs {
		if p.Library(name) == nil && name != SelfLibrary && !requiredName(p, name) {
			problems = append(problems, Problem{Message: fmt.Sprintf("--library %s: the pipeline has no library called %s, and none of its libraries requires one", name, name)})
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	var self *SelfSource
	if selfDir != "" {
		self = &SelfSource{Library: &Library{Name: SelfLibrary, Version: OfflineCommit, Commit: OfflineCommit, RefKind: "directory"}, Read: DirReader(selfDir)}
	}
	out, _, err := ExpandWithSelf(p, func(l *Library, name string) ([]byte, error) {
		dir := l.Dir
		if dir == "" {
			if _, given := dirs[l.Name]; !given && l.Repo == starters.Name {
				// A library's template may use the starters that ship with Launchway, with no directory for them.
				if b, ok := starters.Template(name); ok {
					return b, nil
				}
				return nil, ErrNoTemplate
			}
			dir = dirs[l.Name]
		}
		return TemplateOrStep(DirReader(dir), name, false)
	}, vars, self)
	return out, err
}

func knownDirs(dirs map[string]string) string {
	if len(dirs) == 0 {
		return ""
	}
	var names []string
	for n := range dirs {
		names = append(names, n)
	}
	sort.Strings(names)
	return " (given so far: " + strings.Join(names, ", ") + ")"
}

// requireOffline reads the library.yaml of a library read from a directory and resolves what it requires to the
// directories given for those libraries, and theirs in turn, to the depth templates may be used. A library that
// requires one already on the way down is a circle.
func requireOffline(l *Library, dirs map[string]string, depth int, stack []string) Problems {
	data, err := os.ReadFile(filepath.Join(l.Dir, LibraryManifestPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return Problems{{Message: fmt.Sprintf("reading %s of the library %s: %v", LibraryManifestPath, l.Name, err)}}
	}
	reqs, ps := ParseLibraryManifest(data)
	var out Problems
	for _, p := range ps {
		p.File, p.Library = LibraryManifestPath, l.Name
		out = append(out, p)
	}
	if depth >= MaxNesting {
		return append(out, Problem{Message: fmt.Sprintf("libraries require each other %d deep from %s; at most %d levels are allowed", depth+1, l.Name, MaxNesting)})
	}
	for _, d := range RequiredLibraries(l.Name, reqs) {
		dir, ok := dirs[d.Name]
		switch {
		case !ok:
			out = append(out, Problem{File: LibraryManifestPath, Library: l.Name, Line: d.Line, Message: fmt.Sprintf(
				"the library %s requires %s (%s, version %s); give its directory with --library %s=DIRECTORY", l.Name, d.Name, d.Repo, d.Version, d.Name)})
			continue
		case slices.Contains(stack, dir):
			out = append(out, Problem{File: LibraryManifestPath, Library: l.Name, Line: d.Line, Message: fmt.Sprintf("libraries require each other in a circle: %s requires %s again", l.Name, d.Name)})
			continue
		}
		d.Commit, d.RefKind, d.Dir = OfflineCommit, "directory", dir
		out = append(out, requireOffline(d, dirs, depth+1, append(slices.Clone(stack), dir))...)
		l.Requires = append(l.Requires, d)
	}
	return out
}

// requiredName says whether any library of the pipeline requires a library under this name.
func requiredName(p *Pipeline, name string) bool {
	var walk func(l *Library) bool
	walk = func(l *Library) bool {
		for _, d := range l.Requires {
			if d.Name == name || walk(d) {
				return true
			}
		}
		return false
	}
	for _, l := range p.Libraries {
		if walk(l) {
			return true
		}
	}
	return false
}

// DirReader reads files of a directory by their path from its root, for the
// templates and steps of a library checked out on disk. It never follows a
// link out of the directory.
func DirReader(dir string) func(path string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		f, err := root.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoTemplate
		}
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	}
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
