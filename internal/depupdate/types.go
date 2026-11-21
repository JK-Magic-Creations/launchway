package depupdate

import (
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Ecosystem names. A dependency's Ecosystem is one of these.
const (
	Go        = "go"
	NPM       = "npm"
	Python    = "python"
	Container = "container"
	Workflow  = "workflow"
)

// Scope of a dependency.
const (
	GroupProd = "prod"
	GroupDev  = "dev"
	GroupOpt  = "optional"
	GroupTool = "tool"
)

// Dep is one dependency found in a repository.
type Dep struct {
	Ecosystem  string `json:"ecosystem"`
	Name       string `json:"name"`
	Current    string `json:"current"`              // the version in use (from the lock file when there is one)
	Constraint string `json:"constraint,omitempty"` // the range or tag as written in the manifest
	Digest     string `json:"digest,omitempty"`     // container images: the pinned digest, if any
	Direct     bool   `json:"direct"`               // false: only needed by another dependency
	Group      string `json:"group,omitempty"`      // prod, dev, optional, tool
	File       string `json:"file"`                 // the manifest or lock file, repository-relative
	Line       int    `json:"line,omitempty"`
	LockFile   string `json:"lock_file,omitempty"` // the lock file that records Current, if any
}

// Key identifies a dependency within an ecosystem and manifest.
func (d Dep) Key() string { return d.Ecosystem + "|" + d.File + "|" + d.Name }

// Note is something found that is not updated (yet), shown so nobody
// believes it is covered.
type Note struct {
	File    string `json:"file"`
	Message string `json:"message"`
}

// File is one repository file handed to a Parser.
type File struct {
	Path string // repository-relative, slash separated
	Data []byte
	// Sibling returns the file called name in the same directory, if present.
	Sibling func(name string) ([]byte, bool)
}

// Parser reads one kind of manifest.
type Parser interface {
	Ecosystem() string
	// Detect says whether a repository-relative path is a manifest this
	// parser reads (lock files are read through Sibling, not detected).
	Detect(p string) bool
	Parse(f File) ([]Dep, []Note, error)
}

// Limits bound discovery of one repository.
type Limits struct {
	MaxFiles    int
	MaxFileSize int64
}

// DefaultLimits are used when Discover is given none.
var DefaultLimits = Limits{MaxFiles: 2000, MaxFileSize: 8 << 20}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, "testdata": true, ".venv": true, "venv": true, "target": true}

// Result is everything discovery found.
type Result struct {
	Deps   []Dep    `json:"deps"`
	Notes  []Note   `json:"notes,omitempty"`
	Errors []Note   `json:"errors,omitempty"` // files that could not be read
	Files  []string `json:"files"`            // manifests parsed
}

// Discover walks fsys and parses every manifest the parsers detect. A file
// that cannot be parsed is reported in Errors and does not stop the walk.
func Discover(fsys fs.FS, parsers []Parser, lim Limits) (Result, error) {
	if lim.MaxFiles == 0 {
		lim = DefaultLimits
	}
	var res Result
	count := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != "." && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		for _, pr := range parsers {
			if !pr.Detect(p) {
				continue
			}
			count++
			if count > lim.MaxFiles {
				return fs.SkipAll
			}
			info, err := d.Info()
			if err != nil || info.Size() > lim.MaxFileSize {
				res.Errors = append(res.Errors, Note{p, "file is too large or unreadable"})
				continue
			}
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				res.Errors = append(res.Errors, Note{p, err.Error()})
				continue
			}
			dir := path.Dir(p)
			f := File{Path: p, Data: data, Sibling: func(name string) ([]byte, bool) {
				b, err := fs.ReadFile(fsys, path.Join(dir, name))
				return b, err == nil
			}}
			deps, notes, err := pr.Parse(f)
			if err != nil {
				res.Errors = append(res.Errors, Note{p, err.Error()})
				continue
			}
			res.Files = append(res.Files, p)
			res.Deps = append(res.Deps, deps...)
			res.Notes = append(res.Notes, notes...)
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	sort.SliceStable(res.Deps, func(i, j int) bool {
		a, b := res.Deps[i], res.Deps[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return res, nil
}

// Coming lists the ecosystems that are designed but not built. They are shown
// as coming, never as available.
func Coming() []string {
	return []string{"Maven and Gradle", "Cargo", ".NET (NuGet)", "Helm chart dependencies", "Terraform providers"}
}

// Available lists the ecosystems that work today.
func Available() []string { return []string{Go, NPM, Python, Container, Workflow} }

// IsAvailable reports whether an ecosystem name works today.
func IsAvailable(e string) bool {
	for _, a := range Available() {
		if a == e {
			return true
		}
	}
	return false
}

// Dir returns the directory of a repository-relative path ("" for the root).
func Dir(p string) string {
	d := path.Dir(p)
	if d == "." {
		return ""
	}
	return d
}

// Base returns the file name of a path.
func Base(p string) string { return strings.TrimPrefix(path.Base(p), "./") }
