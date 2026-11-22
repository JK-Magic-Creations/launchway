// Package starters is the library that ships inside Launchway: flight plans
// (whole pipelines for a kind of repository) and step templates (building
// blocks), with the scripts they run as files. A pipeline uses it as
// launchway/<name> with no registration, and its version is Launchway's own:
// the server and every runner of the same release hold identical files.
//
// The files are embedded, so nothing is fetched. A run records the library
// at a content hash (Commit), as it records any library at a Git commit, and
// a runner whose copy has a different hash refuses the run and says to
// update the runner, rather than run other scripts than the server planned.
package starters

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

// Name is what pipelines call the built-in library: uses: launchway/go-service.
const Name = "launchway"

// URL is the address a run records for the built-in library. It is not a Git
// address: runners recognise it and use the files they carry.
const URL = "builtin:launchway"

// Version is the version a pipeline's implicit entry for the library names.
const Version = "builtin"

//go:embed all:library
var files embed.FS

var (
	once   sync.Once
	commit string
)

// Commit is the library's identity: 40 hex digits made from the path and
// contents of every file, in order. It stands where a Git commit would.
func Commit() string {
	once.Do(func() {
		h := sha256.New()
		for _, f := range List() {
			h.Write([]byte(f + "\x00"))
			data, _ := files.ReadFile(path.Join("library", f))
			h.Write(data)
			h.Write([]byte{0})
		}
		commit = hex.EncodeToString(h.Sum(nil))[:40]
	})
	return commit
}

// List returns the path of every file, relative to the library, sorted.
func List() []string {
	var out []string
	_ = fs.WalkDir(files, "library", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, strings.TrimPrefix(p, "library/"))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// File returns one file of the library.
func File(name string) ([]byte, error) { return files.ReadFile(path.Join("library", name)) }

// FS is the library as a file system, rooted at the library.
func FS() fs.FS {
	sub, _ := fs.Sub(files, "library")
	return sub
}

// Template returns the file of a template, or false.
func Template(name string) ([]byte, bool) {
	if strings.ContainsAny(name, "/\\.") || name == "" {
		return nil, false
	}
	data, err := files.ReadFile("library/templates/" + name + ".yaml")
	return data, err == nil
}

// Templates lists the template names, sorted.
func Templates() []string {
	ents, _ := fs.ReadDir(files, "library/templates")
	var out []string
	for _, e := range ents {
		if n, ok := strings.CutSuffix(e.Name(), ".yaml"); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// IsExecutable says a file of the library is written executable: scripts.
func IsExecutable(name string) bool { return strings.HasSuffix(name, ".sh") }

// Nearest returns the template whose name a mistyped one is closest to (two
// edits at most), or "".
func Nearest(name string) string {
	best, bestD := "", 3
	for _, t := range Templates() {
		if d := distance(name, t); d < bestD {
			best, bestD = t, d
		}
	}
	return best
}

func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Step returns the manifest of a custom step of the library (steps/<name>/step.yaml), or false.
func Step(name string) ([]byte, bool) {
	if strings.ContainsAny(name, "/\\.") || name == "" {
		return nil, false
	}
	data, err := files.ReadFile("library/steps/" + name + "/step.yaml")
	return data, err == nil
}

// Steps lists the custom step names, sorted.
func Steps() []string {
	ents, _ := fs.ReadDir(files, "library/steps")
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
