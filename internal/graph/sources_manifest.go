package graph

import (
	"context"
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/depupdate"
	"github.com/jk-magic-creations/launchway/internal/depupdate/ecosystems"
)

// ManifestSource reads a repository's package manifests with the parsers the
// dependency updater already uses, and says what the repository publishes
// (the module, the npm package, the Python distribution) and what it depends
// on. A package is one node whoever mentions it: the repository that
// publishes it and every repository that depends on it meet in that node,
// which is what puts repositories hosted in the same installation in
// dependency order with no further configuration.
type ManifestSource struct {
	Repos Repos
}

// Name is "manifests".
func (ManifestSource) Name() string { return "manifests" }

// Scopes is repositories.
func (ManifestSource) Scopes() []string { return []string{ScopeRepository} }

var (
	goModuleRe = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)"?`)
	pyNameRe   = regexp.MustCompile(`(?m)^name\s*=\s*["']([^"']+)["']`)
	pyNormRe   = regexp.MustCompile(`[-_.]+`)
)

// PythonName normalises a Python distribution name the way installers do.
func PythonName(n string) string { return strings.ToLower(pyNormRe.ReplaceAllString(n, "-")) }

// PackageKey is the key of a package node: ecosystem, a colon, the name.
func PackageKey(eco, name string) string {
	if eco == depupdate.Python {
		name = PythonName(name)
	}
	return eco + ":" + name
}

// identity reads what a manifest publishes, if it says.
func identity(p string, data []byte) (eco, name string) {
	switch path.Base(p) {
	case "go.mod":
		if m := goModuleRe.FindSubmatch(data); m != nil {
			return depupdate.Go, string(m[1])
		}
	case "package.json":
		var pj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &pj) == nil && pj.Name != "" {
			return depupdate.NPM, pj.Name
		}
	case "pyproject.toml":
		txt := string(data)
		for _, hdr := range []string{"[project]", "[tool.poetry]"} {
			if i := strings.Index(txt, hdr); i >= 0 {
				rest := txt[i+len(hdr):]
				if j := strings.Index(rest, "\n["); j >= 0 {
					rest = rest[:j]
				}
				if m := pyNameRe.FindStringSubmatch(rest); m != nil {
					return depupdate.Python, m[1]
				}
			}
		}
	}
	return "", ""
}

func componentKey(repo, dir string) string { return repo + ":" + dir }

func dirOf(p string) string {
	d := path.Dir(p)
	if d == "" {
		return "."
	}
	return d
}

// Assert reads the repository.
func (m ManifestSource) Assert(ctx context.Context, sc Scope) (Assertion, string, error) {
	var a Assertion
	tree, commit, _, err := repoFiles(ctx, m.Repos, sc.Ref, ExtraGlobs)
	if err != nil {
		return a, "", err
	}
	sub := RepoSubject(sc.Ref)
	repo := NodeRef{KindRepository, sc.Ref}
	a.Add(Node{Kind: KindRepository, Key: sc.Ref, Name: sc.Ref, Subject: sub, Attrs: map[string]any{"commit": commit}})
	res, err := depupdate.Discover(tree, ecosystems.Parsers(nil), depupdate.Limits{})
	if err != nil {
		return a, commit, err
	}
	sort.Strings(res.Files)
	comps := map[string]bool{}
	ensure := func(manifest, eco string) NodeRef {
		dir := dirOf(manifest)
		key := componentKey(sc.Ref, dir)
		if !comps[key] {
			comps[key] = true
			name := sc.Ref
			if dir != "." {
				name += "/" + dir
			}
			a.Add(Node{Kind: KindComponent, Key: key, Name: name, Subject: sub, Attrs: map[string]any{"ecosystem": eco, "dir": dir, "manifest": manifest}})
			a.Link(Edge{Kind: EdgeContains, From: repo, To: NodeRef{KindComponent, key}, Subject: sub, Ref: commit})
		}
		return NodeRef{KindComponent, key}
	}
	for _, f := range res.Files {
		eco := ecoOfFile(f)
		if eco == "" {
			continue
		}
		data, err := fs.ReadFile(tree, f)
		if err != nil {
			continue
		}
		c := ensure(f, eco)
		if e, name := identity(f, data); name != "" {
			pk := PackageKey(e, name)
			a.Add(Node{Kind: KindPackage, Key: pk, Name: name, Attrs: map[string]any{"ecosystem": e, "published_by": sc.Ref}})
			a.Link(Edge{Kind: EdgeContains, From: c, To: NodeRef{KindPackage, pk}, Qualifier: f, Subject: sub, Ref: commit})
		}
	}
	for _, d := range res.Deps {
		if !d.Direct || d.Ecosystem == depupdate.Container || d.Ecosystem == depupdate.Workflow {
			continue
		}
		c := ensure(d.File, d.Ecosystem)
		pk := PackageKey(d.Ecosystem, d.Name)
		a.Add(Node{Kind: KindPackage, Key: pk, Name: d.Name, Attrs: map[string]any{"ecosystem": d.Ecosystem}})
		con := d.Constraint
		if con == "" {
			con = d.Current
		}
		attrs := map[string]any{"group": d.Group}
		if d.Digest != "" {
			attrs["digest"] = d.Digest
		}
		if c.Key == pk {
			continue
		}
		a.Link(Edge{Kind: EdgeDependsOn, From: c, To: NodeRef{KindPackage, pk}, Qualifier: d.File, Constraint: con, Resolved: d.Current, Attrs: attrs, Subject: sub, Ref: commit})
	}
	// Owners from CODEOWNERS: the default rule's owners own the repository.
	for _, p := range []string{"CODEOWNERS", ".github/CODEOWNERS", "docs/CODEOWNERS"} {
		data, err := fs.ReadFile(tree, p)
		if err != nil {
			continue
		}
		for _, o := range defaultOwners(string(data)) {
			a.Add(Node{Kind: KindOwner, Key: o, Name: o})
			a.Link(Edge{Kind: EdgeOwnedBy, From: repo, To: NodeRef{KindOwner, o}, Subject: sub, Ref: commit})
		}
		break
	}
	return a, commit, nil
}

func ecoOfFile(p string) string {
	b := path.Base(p)
	switch {
	case b == "go.mod":
		return depupdate.Go
	case b == "package.json":
		return depupdate.NPM
	case b == "pyproject.toml" || (strings.HasPrefix(strings.ToLower(b), "requirements") && (strings.HasSuffix(b, ".txt") || strings.HasSuffix(b, ".in"))):
		return depupdate.Python
	}
	return ""
}

// defaultOwners returns the owners of the rule that matches everything.
func defaultOwners(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		f := strings.Fields(strings.SplitN(l, "#", 2)[0])
		if len(f) >= 2 && f[0] == "*" {
			out = append([]string{}, f[1:]...)
		}
	}
	return out
}
