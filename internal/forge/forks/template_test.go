package forks

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/forge"
)

const manifestText = `
variables:
  - name: project
    description: The name of the project
    pattern: '[a-z][a-z0-9-]{1,30}'
  - name: author
    description: Who wrote it
    default: Anonymous
contents: ["**/*.md", "src/**"]
names: true
`

func (w *world) template(vis forge.Visibility) forge.Repository {
	w.t.Helper()
	t := w.repo("acme", "starter", vis)
	w.commit(t, "main", map[string]string{
		ManifestPath:             manifestText,
		"README.md":              "# {{project}}\nBy {{author}}. {{unknown}} stays. {{ project }} stays.\n",
		"src/{{project}}/app.go": "package {{project}}\n",
		"notes.txt":              "{{project}} is not in the contents patterns\n",
	}, "template v1")
	w.commit(t, "legacy", map[string]string{"legacy.md": "old {{project}}\n"}, "legacy branch")
	if err := w.svc.SetTemplate(w.ctx, t.ID, true); err != nil {
		w.t.Fatal(err)
	}
	return t
}

func TestManifestIsCheckedBeforeAnythingIsMade(t *testing.T) {
	bad := map[string]string{
		"a bad name":        "variables:\n  - name: Project\n",
		"a duplicate":       "variables:\n  - name: a\n  - name: a\n",
		"a bad pattern":     "variables:\n  - name: a\n    pattern: '('\n",
		"an unknown field":  "variables: []\nrun: rm -rf /\n",
		"a default failing": "variables:\n  - name: a\n    pattern: '[0-9]+'\n    default: x\n",
		"a bad glob":        "contents: ['/etc/*']\n",
	}
	for what, text := range bad {
		if _, err := ParseManifest([]byte(text)); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
	m, err := ParseManifest([]byte(manifestText))
	if err != nil || len(m.Variables) != 2 || !m.Names {
		t.Fatalf("%+v %v", m, err)
	}
	vals, err := m.Resolve(map[string]string{"project": "my-app"})
	if err != nil || vals["author"] != "Anonymous" {
		t.Fatalf("%v %v", vals, err)
	}
	for what, in := range map[string]map[string]string{
		"a missing value":  {},
		"an unknown name":  {"project": "ok", "other": "x"},
		"a pattern miss":   {"project": "Not Ok"},
		"a traversal":      {"project": "a/../b"},
		"a separator":      {"project": "a/b"},
		"a line break":     {"project": "ok", "author": "x\ny"},
		"too long a value": {"project": "ok", "author": strings.Repeat("x", 101)},
	} {
		if _, err := m.Resolve(in); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
}

func TestUseTemplateMakesOneCommitWithTheDeclaredVariablesReplaced(t *testing.T) {
	w := newWorld(t)
	tpl := w.template(forge.Public)
	child, err := w.svc.Use(w.ctx, tpl, NewFromTemplate{Owner: "ada", Name: "demo", By: "ada", Values: map[string]string{"project": "demo-app", "author": "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if child.Visibility != forge.Public || child.DefaultBranch != "main" {
		t.Fatalf("%+v", child)
	}
	refs := w.refs(child)
	if len(refs) != 1 || refs["refs/heads/main"] == "" {
		t.Fatalf("only the default branch comes by default: %v", refs)
	}
	if n := w.gitIn(child, "rev-list", "--count", "main"); n != "1" {
		t.Fatalf("%s commits, want 1 (no history)", n)
	}
	if p := w.gitIn(child, "rev-list", "--parents", "-n", "1", "main"); len(strings.Fields(p)) != 1 {
		t.Fatalf("the commit has a parent: %s", p)
	}
	if got := w.gitIn(child, "show", "main:README.md"); got != "# demo-app\nBy Ada. {{unknown}} stays. {{ project }} stays." {
		t.Fatalf("README: %q", got)
	}
	if got := w.gitIn(child, "show", "main:src/demo-app/app.go"); got != "package demo-app" {
		t.Fatalf("a file with a substituted name: %q", got)
	}
	if got := w.gitIn(child, "show", "main:notes.txt"); !strings.Contains(got, "{{project}}") {
		t.Fatalf("a file outside the content patterns was changed: %q", got)
	}
	if out := w.gitIn(child, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, "template.yaml") || strings.Contains(out, "{{") {
		t.Fatalf("the new tree holds %q", out)
	}
	if got := w.gitIn(child, "log", "-1", "--format=%an|%s", "main"); got != "ada|Initial commit from the template acme/starter" {
		t.Fatalf("the commit: %q", got)
	}
	// The template's history is not in the new repository.
	if out := w.gitIn(child, "cat-file", "--batch-all-objects", "--batch-check"); strings.Count(out, " commit ") != 1 {
		t.Fatalf("objects:\n%s", out)
	}
}

func TestUseTemplateWithAllBranchesAndTheVisibilityCeiling(t *testing.T) {
	w := newWorld(t)
	tpl := w.template(forge.Private)
	vals := map[string]string{"project": "demo"}
	if _, err := w.svc.Use(w.ctx, tpl, NewFromTemplate{Owner: "ada", Name: "pub", By: "ada", Values: vals, Visibility: forge.Public}); !errors.Is(err, ErrVisibility) {
		t.Fatalf("a public repository from a private template: %v", err)
	}
	if _, err := w.prov.Repositories().Get(w.ctx, "ada/pub"); err == nil {
		t.Error("a refused use left a repository")
	}
	child, err := w.svc.Use(w.ctx, tpl, NewFromTemplate{Owner: "ada", Name: "all", By: "ada", Values: vals, IncludeBranches: true})
	if err != nil {
		t.Fatal(err)
	}
	refs := w.refs(child)
	if len(refs) != 2 || refs["refs/heads/legacy"] == "" {
		t.Fatalf("%v", refs)
	}
	if got := w.gitIn(child, "show", "legacy:legacy.md"); got != "old demo" {
		t.Fatalf("%q", got)
	}
	if n := w.gitIn(child, "rev-list", "--count", "legacy"); n != "1" {
		t.Fatalf("the other branch has %s commits", n)
	}
}

func TestUseTemplateRefusesWhatCouldEscapeTheNewRepository(t *testing.T) {
	w := newWorld(t)
	tpl := w.template(forge.Public)
	for _, v := range []string{"../escape", "a/b", "..", ".git", "x\x00y"} {
		if _, err := w.svc.Use(w.ctx, tpl, NewFromTemplate{Owner: "ada", Name: "bad", By: "ada", Values: map[string]string{"project": v}}); err == nil {
			t.Errorf("the value %q was accepted", v)
		}
	}
	if _, err := w.prov.Repositories().Get(w.ctx, "ada/bad"); err == nil {
		t.Error("a refused use left a repository")
	}
	plain := w.repo("acme", "plain", forge.Public)
	w.commit(plain, "main", map[string]string{"a": "b"}, "x")
	if _, err := w.svc.Use(w.ctx, plain, NewFromTemplate{Owner: "ada", Name: "n", By: "ada"}); !errors.Is(err, ErrRelation) {
		t.Fatalf("a repository that is no template: %v", err)
	}
	if !safeForTest("a/b") || safeForTest("a/../b") || safeForTest(".git/config") || safeForTest("/abs") || safeForTest("a//b") || safeForTest("x/.GIT/y") {
		t.Error("safePath is wrong")
	}
}

func safeForTest(p string) bool { return safePath(p) == nil }
