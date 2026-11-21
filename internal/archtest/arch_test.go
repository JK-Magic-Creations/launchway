// Package archtest holds the architecture and size tests of the repository.
// It has no production code: the tests read the source tree and fail when a
// change breaks a boundary or grows something already too big. See
// docs/development/quality-gates.md.
package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/jk-magic-creations/launchway/"

// allowedImports is the import graph of today (20 November 2026, schema 22):
// for each package, the internal packages it may import. A package or edge
// that is not in this table fails TestImportsAreInTheTable. To add an edge,
// add it here in the same pull request and say why; to remove one, delete it
// (TestTableHasNoStaleEdges makes the table shrink as the code is cleaned up).
// Edges the audit calls harmful may not be added at all: see forbidden below.
var allowedImports = map[string][]string{
	"cmd/launchway":                     {"internal/ai", "internal/api", "internal/auth", "internal/blobstore", "internal/config", "internal/deploy/argocd", "internal/engine", "internal/executor", "internal/forge/storage", "internal/gitexec", "internal/imagebuild", "internal/pipeline", "internal/runner", "internal/secrets", "internal/source", "internal/verify/source"},
	"internal/accept":                   {},
	"internal/ai":                       {"internal/pipeline"},
	"internal/ai/aitest":                {},
	"internal/api":                      {"internal/ai", "internal/auth", "internal/engine", "internal/hub", "internal/ids", "internal/pipeline", "internal/policy", "internal/redact", "internal/resources", "internal/runnerauth", "internal/secrets", "internal/source", "internal/verify"},
	"internal/auth":                     {},
	"internal/blobstore":                {},
	"internal/config":                   {"internal/accept", "internal/ai", "internal/auth", "internal/blobstore", "internal/deploy/argocd", "internal/engine", "internal/executor", "internal/imagebuild", "internal/pipeline", "internal/policy", "internal/secrets", "internal/source", "internal/verify/source"},
	"internal/coverage":                 {},
	"internal/deploy":                   {"internal/pipeline"},
	"internal/deploy/argocd":            {"internal/deploy", "internal/deploy/edit", "internal/deploy/gitops", "internal/engine", "internal/pipeline", "internal/secrets"},
	"internal/deploy/edit":              {},
	"internal/deploy/gitops":            {"internal/gitexec", "internal/redact"},
	"internal/engine":                   {"internal/accept", "internal/ai", "internal/blobstore", "internal/coverage", "internal/deploy", "internal/deploy/edit", "internal/findings", "internal/hub", "internal/ids", "internal/pipeline", "internal/policy", "internal/redact", "internal/resources", "internal/runnerauth", "internal/sbom", "internal/testreport", "internal/verify", "internal/verify/source"},
	"internal/executor":                 {"internal/resources"},
	"internal/fakeidp":                  {},
	"internal/forge":                    {},
	"internal/forge/forgetest":          {"internal/forge"},
	"internal/forge/providers/plain":    {"internal/forge", "internal/forge/store", "internal/gitexec", "internal/source"},
	"internal/forge/storage":            {"internal/blobstore", "internal/engine", "internal/gitexec"},
	"internal/forge/storagespike":       {},
	"internal/forge/store":              {"internal/forge", "internal/ids"},
	"internal/gitexec":                  {"internal/redact"},
	"internal/findings":                 {"internal/findings/jsonscan"},
	"internal/findings/jsonscan":        {},
	"internal/hub":                      {},
	"internal/ids":                      {},
	"internal/imagebuild":               {"internal/findings", "internal/pipeline"},
	"internal/imagebuild/imagetest":     {},
	"internal/pipeline":                 {"internal/deploy/edit", "internal/resources"},
	"internal/policy":                   {},
	"internal/redact":                   {},
	"internal/resources":                {},
	"internal/runner":                   {"internal/accept", "internal/ai", "internal/deploy/argocd", "internal/engine", "internal/executor", "internal/gitexec", "internal/imagebuild", "internal/pipeline", "internal/redact", "internal/runnerauth", "internal/secrets", "internal/verify", "internal/verify/source"},
	"internal/runnerauth":               {},
	"internal/sbom":                     {"internal/findings/jsonscan"},
	"internal/secrets":                  {"internal/pipeline"},
	"internal/source":                   {"internal/engine", "internal/forge", "internal/forge/store", "internal/gitexec", "internal/pipeline", "internal/policy", "internal/redact"},
	"internal/testdb":                   {},
	"internal/testlib":                  {},
	"internal/testreport":               {},
	"internal/verify":                   {},
	"internal/verify/source":            {"internal/pipeline", "internal/verify"},
	"internal/verify/source/sourcetest": {},
}

// rule forbids a family of edges. A rule is stronger than the table: an edge
// that matches a rule fails even when it is in the table, unless it is listed
// in knownExceptions.
type rule struct {
	why   string
	from  func(pkg string) bool
	to    func(pkg string) bool
	label string
}

func in(set ...string) func(string) bool {
	return func(p string) bool {
		for _, s := range set {
			if p == s {
				return true
			}
		}
		return false
	}
}

func under(prefixes ...string) func(string) bool {
	return func(p string) bool {
		for _, s := range prefixes {
			if p == s || strings.HasPrefix(p, s+"/") {
				return true
			}
		}
		return false
	}
}

// leaves import nothing internal (audit 2.1). Keep them that way: they are
// what every other package may depend on.
var leaves = []string{
	"internal/redact", "internal/ids", "internal/resources", "internal/hub",
	"internal/policy", "internal/auth", "internal/verify", "internal/testreport",
	"internal/blobstore", "internal/coverage", "internal/runnerauth", "internal/accept", "internal/forge",
}

// runnerSide packages run on the runner or are linked into it. They must not
// depend on the server's engine: wire types belong in a small internal/wire
// package (audit problem 2, step 5), not in engine.
var runnerSide = []string{
	"internal/runner", "internal/executor", "internal/imagebuild", "internal/verify",
	"internal/deploy", "internal/secrets", "internal/redact", "internal/resources",
}

var forbidden = []rule{
	{
		label: "leaf packages import nothing internal",
		why:   "leaf packages are the foundation; an internal import turns them into a node of the graph",
		from:  in(leaves...),
		to:    under("internal"),
	},
	{
		label: "runner-side packages must not import internal/engine",
		why:   "the runner binary would link the whole server; move the shared types to internal/wire",
		from:  under(runnerSide...),
		to:    in("internal/engine"),
	},
	{
		label: "engine must not import api, runner, source, config or cmd",
		why:   "engine is below its callers; use an interface the caller implements",
		from:  under("internal/engine"),
		to:    in("internal/api", "internal/runner", "internal/source", "internal/config", "cmd/launchway"),
	},
	{
		label: "api must not import config, runner or cmd",
		why:   "api receives its dependencies from the composition root; it must not load configuration or run steps",
		from:  under("internal/api"),
		to:    in("internal/config", "internal/runner", "cmd/launchway"),
	},
	{
		label: "nothing but cmd imports config",
		why:   "config is a composition root; only the binary may depend on it",
		from: func(p string) bool {
			return p != "cmd/launchway" && p != "internal/config"
		},
		to: in("internal/config"),
	},
	{
		label: "clients of the forge never import a provider",
		why:   "the API, MCP server and screens see only internal/forge (doc 26 section 3.4); providers are wired in by cmd through the registry",
		from:  func(p string) bool { return p != "cmd/launchway" && !strings.HasPrefix(p, "internal/forge/") },
		to:    func(p string) bool { return strings.HasPrefix(p, "internal/forge/providers/") },
	},
	{
		label: "nothing imports cmd",
		why:   "cmd is the top of the graph",
		from:  func(p string) bool { return p != "cmd/launchway" },
		to:    in("cmd/launchway"),
	},
	{
		label: "production code must not import test support packages",
		why:   "testdb, testlib, fakeidp and the *test packages are for tests only",
		from: func(p string) bool {
			return !strings.HasSuffix(p, "test") && !strings.HasSuffix(p, "integration") &&
				p != "internal/testdb" && p != "internal/testlib" && p != "internal/fakeidp"
		},
		to: func(p string) bool {
			return p == "internal/testdb" || p == "internal/testlib" || p == "internal/fakeidp" ||
				strings.HasSuffix(p, "/aitest") || strings.HasSuffix(p, "/imagetest") || strings.HasSuffix(p, "/sourcetest")
		},
	},
}

// knownExceptions are today's violations of the rules above. They may only
// shrink: TestKnownExceptionsAreStillNeeded fails when one disappears, so it
// is deleted from here.
var knownExceptions = map[string]string{
	// TODO(audit step 5): extract internal/wire (Job, Result, CollectedReports,
	// ...), alias it from engine, switch the runner to it, then delete these.
	"internal/runner -> internal/engine": "runner uses engine.Job/Result/CollectedReports (wire types live in engine)",
	// TODO(audit step 5): argocd needs a callback interface instead of engine.
	"internal/deploy/argocd -> internal/engine": "argocd deploy driver uses engine types; engine imports deploy (proxy cycle)",
}

// forbiddenEdgeExceptions lists exceptions for edges that match a rule other
// than runner-side->engine. Keyed the same way as knownExceptions.
func exceptionFor(from, to string) (string, bool) {
	why, ok := knownExceptions[from+" -> "+to]
	return why, ok
}

// graph reads the non-test imports of every package under internal/ and cmd/.
func graph(t *testing.T) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	g := map[string][]string{}
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			pkg := filepath.ToSlash(rel)
			if _, ok := g[pkg]; !ok {
				g[pkg] = nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if strings.HasPrefix(p, modulePath+"internal/") || strings.HasPrefix(p, modulePath+"cmd/") {
					g[pkg] = appendUnique(g[pkg], strings.TrimPrefix(p, modulePath))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for k := range g {
		sort.Strings(g[k])
	}
	return g
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			t.Fatal("go.mod not found above " + dir)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// TestImportsAreInTheTable fails on an import edge, or a whole package, that
// is not in allowedImports.
func TestImportsAreInTheTable(t *testing.T) {
	for pkg, imports := range graph(t) {
		allowed, known := allowedImports[pkg]
		if !known {
			t.Errorf("new package %s: add it to allowedImports in internal/archtest/arch_test.go with the internal packages it may import", pkg)
			continue
		}
		for _, imp := range imports {
			if !contains(allowed, imp) {
				t.Errorf("%s imports %s, which is not in the allowed table. If the edge is right, add it to allowedImports and explain why in the pull request; otherwise move the code", pkg, imp)
			}
		}
	}
}

// TestTableHasNoStaleEdges keeps the table honest: an edge that no longer
// exists must be removed, so the table only shrinks.
func TestTableHasNoStaleEdges(t *testing.T) {
	g := graph(t)
	for pkg, allowed := range allowedImports {
		actual, exists := g[pkg]
		if !exists {
			t.Errorf("%s is in allowedImports but the package no longer exists: delete the row", pkg)
			continue
		}
		for _, imp := range allowed {
			if !contains(actual, imp) {
				t.Errorf("%s no longer imports %s: delete the edge from allowedImports", pkg, imp)
			}
		}
	}
}

// TestForbiddenEdges applies the rules; known exceptions pass.
func TestForbiddenEdges(t *testing.T) {
	for pkg, imports := range graph(t) {
		for _, imp := range imports {
			for _, r := range forbidden {
				if !r.from(pkg) || !r.to(imp) {
					continue
				}
				if _, ok := exceptionFor(pkg, imp); ok {
					continue
				}
				t.Errorf("%s -> %s breaks the rule %q: %s", pkg, imp, r.label, r.why)
			}
		}
	}
}

// TestKnownExceptionsAreStillNeeded makes the exception list shrink: once the
// edge is gone, its entry must go too, so it cannot come back unnoticed.
func TestKnownExceptionsAreStillNeeded(t *testing.T) {
	g := graph(t)
	for edge := range knownExceptions {
		parts := strings.Split(edge, " -> ")
		if !contains(g[parts[0]], parts[1]) {
			t.Errorf("the exception %q no longer applies: delete it from knownExceptions", edge)
		}
	}
}
