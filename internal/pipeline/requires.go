package pipeline

// Libraries that use libraries. A library's templates may use the templates of the starters that ship with
// Launchway (uses: launchway/go-test) and of libraries the library requires, each at a version it pins, in a
// file at the root of the library:
//
//	# library.yaml
//	requires:
//	  tools: { repo: shared-tools, version: v2.1.0 }
//
// A template of that library then writes uses: tools/lint. What a requirement points at is resolved to a commit
// when the pipeline is expanded, and the resolved libraries are listed with the pipeline's own (required_by
// names the library that asked for each), so that a run, a resume and every runner use exactly those files.
// The version is a tag or a full commit, never a branch: a library's pins do not move by themselves.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/starters"
)

// LibraryManifestPath is the file of a library that says what it requires.
const LibraryManifestPath = "library.yaml"

// MaxRequires bounds what one library may require.
const MaxRequires = 10

// Requirement is a library another library requires, under the name its templates use it by.
type Requirement struct {
	Name, Repo, Version string
	Line                int
}

// ParseLibraryManifest reads a library's library.yaml. Problems are about that file.
func ParseLibraryManifest(data []byte) ([]Requirement, Problems) {
	if len(data) > MaxFileBytes {
		return nil, Problems{{Message: fmt.Sprintf("%s is larger than %d KiB", LibraryManifestPath, MaxFileBytes>>10)}}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, Problems{syntaxProblem(err)}
	}
	ps := &parser{}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	var out []Requirement
	if root.Kind != yaml.MappingNode {
		return nil, Problems{{Line: root.Line, Message: LibraryManifestPath + " is a mapping with requires: in it"}}
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		switch {
		case k.Value != "requires":
			ps.add(k, k.Value, "unknown key %q; the only key of %s is requires", clipText(k.Value, 40), LibraryManifestPath)
		case v.Kind != yaml.MappingNode:
			ps.add(v, "requires", "requires is a mapping of the names templates use to libraries: tools: { repo: shared-tools, version: v2.1.0 }")
		case len(v.Content)/2 > MaxRequires:
			ps.add(v, "requires", "a library requires at most %d others", MaxRequires)
		default:
			for j := 0; j+1 < len(v.Content); j += 2 {
				out = append(out, ps.requirement(v.Content[j], v.Content[j+1]))
			}
		}
	}
	if len(ps.problems) > 0 {
		return nil, ps.problems
	}
	return out, nil
}

func (ps *parser) requirement(k, v *yaml.Node) Requirement {
	at := "requires." + k.Value
	r := Requirement{Name: k.Value, Line: k.Line}
	switch {
	case !libNameRe.MatchString(k.Value):
		ps.add(k, at, "%q is not a library name: use lower-case letters, digits and hyphens, starting with a letter (at most 40)", clipText(k.Value, 60))
	case k.Value == starters.Name:
		ps.add(k, at, "launchway is the starters that ship with Launchway, which every library may use; require a library of your own under another name")
	}
	ps.eachKey(v, at, []string{"repo", "version"}, func(k2, v2 *yaml.Node) {
		if k2.Value == "repo" {
			r.Repo = ps.str(v2, at+".repo")
		} else {
			r.Version = ps.str(v2, at+".version")
		}
	})
	if v.Kind == yaml.MappingNode {
		switch {
		case r.Repo == "":
			ps.add(v, at+".repo", "name the library, as the server's configuration lists it")
		case !ValidVersion(r.Version):
			ps.add(v, at+".version", "pin the version: a tag or a full commit id, such as v2.1.0")
		case !IsCommitID(r.Version) && ps.looksLikeBranch(r.Version):
			ps.add(v, at+".version", "pin the version: a tag or a full commit id; %q reads as a branch, which moves", clipText(r.Version, 60))
		}
	}
	return r
}

// looksLikeBranch says whether a version is surely not a tag: the usual names of branches. The server, which can
// tell, refuses any version that resolves to a branch.
func (ps *parser) looksLikeBranch(v string) bool {
	switch strings.ToLower(v) {
	case "main", "master", "develop", "trunk", "head":
		return true
	}
	return false
}

// RequiredLibraries turns a library's requirements into libraries to resolve: the pins as written, no commit yet.
func RequiredLibraries(parent string, reqs []Requirement) []*Library {
	out := make([]*Library, len(reqs))
	for i, r := range reqs {
		out[i] = &Library{Name: r.Name, Repo: r.Repo, Version: r.Version, Line: r.Line, RequiredBy: parent}
	}
	return out
}

// builtinLibrary is the starters that ship with Launchway as a library of a run.
func builtinLibrary(name string) *Library {
	return &Library{Name: name, Repo: starters.Name, Version: starters.Version, Commit: starters.Commit(), RefKind: "builtin"}
}

// libraryFor finds the library a uses step of a template names, and the template in it: a template of the same
// library by name alone, launchway/<name> for a starter, <alias>/<name> for a library this one requires. It
// reports why it cannot, and returns nil. In the pipeline's own steps the library is one it lists.
func (x *expander) libraryFor(sc *scope, root *Pipeline, s *Step, at string) (*Library, string) {
	alias, name, cross := strings.Cut(s.Uses, "/")
	if sc.lib == nil {
		return root.Library(alias), name
	}
	if !cross {
		return sc.lib, s.Uses
	}
	if alias == starters.Name {
		return x.registered(sc.lib, "", builtinLibrary(starters.Name)), name
	}
	for _, d := range sc.lib.Requires {
		if d.Name == alias {
			return x.registered(sc.lib, sc.lib.Name, d), name
		}
	}
	var have []string
	for _, d := range sc.lib.Requires {
		have = append(have, d.Name)
	}
	sort.Strings(have)
	list := "it requires none: add requires: to " + LibraryManifestPath + " at its root"
	if len(have) > 0 {
		list = "it requires " + strings.Join(have, ", ")
	}
	x.problem(sc, s.usesLine, 0, at+".uses", "the library %s has no requirement called %q (%s); the starters are launchway/<name>", sc.lib.Name, clipText(alias, 40), list)
	return nil, ""
}

// registered adds a library a template asked for to the run's libraries, once: under the name its requirer
// calls it if that is free, else under a name made unique, so that no two libraries of a run share a name.
// requiredBy is "" for the starters.
func (x *expander) registered(parent *Library, requiredBy string, dep *Library) *Library {
	key := dep.Repo + "@" + dep.Commit
	if l, ok := x.required[key]; ok {
		return l
	}
	for _, l := range x.out.Libraries {
		if l.Repo == dep.Repo && l.Commit == dep.Commit && l.Version == dep.Version {
			return l
		}
	}
	name := dep.Name
	for n := 2; x.libraryNamed(name); n++ {
		name = dep.Name + "-" + strconv.Itoa(n)
	}
	c := *dep
	c.Name, c.RequiredBy = name, requiredBy
	if x.required == nil {
		x.required = map[string]*Library{}
	}
	x.required[key] = &c
	x.out.Libraries = append(x.out.Libraries, &c)
	return &c
}

func (x *expander) libraryNamed(name string) bool {
	for _, l := range x.out.Libraries {
		if l.Name == name {
			return true
		}
	}
	return false
}
