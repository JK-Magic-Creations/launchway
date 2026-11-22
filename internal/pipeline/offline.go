package pipeline

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
		l.Commit, l.RefKind = OfflineCommit, "directory"
	}
	for name := range dirs {
		if p.Library(name) == nil && name != SelfLibrary {
			problems = append(problems, Problem{Message: fmt.Sprintf("--library %s: the pipeline has no library called %s", name, name)})
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	var self *SelfSource
	if selfDir != "" {
		self = &SelfSource{Library: &Library{Name: SelfLibrary, Version: OfflineCommit, Commit: OfflineCommit, RefKind: "directory"}, Read: dirReader(selfDir)}
	}
	out, _, err := ExpandWithSelf(p, func(l *Library, name string) ([]byte, error) {
		return TemplateOrStep(dirReader(dirs[l.Name]), name, false)
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

// dirReader reads files of a directory by their path from its root, for the
// templates and steps of a library checked out on disk. It never follows a
// link out of the directory.
func dirReader(dir string) func(path string) ([]byte, error) {
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
