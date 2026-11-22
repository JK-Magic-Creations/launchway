package source

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
	"github.com/jk-magic-creations/launchway/internal/testlib"
)

// requireSetup is a library (ci-templates) whose template uses a starter and a template of a library it requires
// (shared-tools, at the tag v2.1.0), an application that uses it, and a manager that knows all three.
func requireSetup(t *testing.T, manifest string) (*Manager, *testlib.Repo, *testlib.Repo) {
	t.Helper()
	dir := t.TempDir()
	tools := testlib.New(t, filepath.Join(dir, "tools"), map[string]string{
		"templates/lint.yaml": "template: lint\nsteps:\n  run: { image: alpine:3.22, run: \"$LAUNCHWAY_LIB/scripts/lint.sh\" }\n",
		"scripts/lint.sh":     "echo lint\n",
	})
	tools.Tag("v2.1.0", tools.Head())
	lib := testlib.New(t, filepath.Join(dir, "lib"), map[string]string{
		"templates/outer.yaml": "template: outer\nsteps:\n  lint: { uses: tools/lint }\n  secrets: { uses: launchway/secret-scan, needs: lint }\n",
		"library.yaml":         manifest,
	})
	lib.Tag("v1", lib.Head())
	app := testlib.New(t, filepath.Join(dir, "app"), map[string]string{
		"README":                   "app",
		".launchway/pipeline.yaml": "pipeline: app\non: { push: { branches: [main] } }\nlibraries:\n  platform: { repo: ci-templates, version: v1 }\nsteps:\n  ci:\n    uses: platform/outer\n",
	})
	eng := newTestEngine(t, testdb.New(t))
	m, err := NewManagerWithLibraries(eng, filepath.Join(dir, "data"), []Project{{Name: "app", URL: app.Bare}},
		[]Library{{Name: "ci-templates", URL: lib.Bare}, {Name: "shared-tools", URL: tools.Bare}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m, tools, lib
}

func TestServerResolvesWhatALibraryRequires(t *testing.T) {
	m, tools, _ := requireSetup(t, "requires:\n  tools: { repo: shared-tools, version: v2.1.0 }\n")
	r, err := m.StartRun(context.Background(), "app", "main", "dana", nil)
	if err != nil {
		t.Fatal(err)
	}
	d, err := m.eng.GetRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*pipeline.Library{}
	for _, l := range d.Graph.Libraries {
		got[l.Name] = l
	}
	if got["tools"] == nil || got["tools"].Commit != tools.Head() || got["tools"].RequiredBy != "platform" || got["tools"].RefKind != "tag" || got["launchway"] == nil {
		t.Fatalf("the pipeline's libraries, with the required ones resolved: %+v", d.Graph.Libraries)
	}
	byName := map[string]engine.LibrarySource{}
	for _, l := range d.Source.Libraries {
		byName[l.Name] = l
	}
	if byName["tools"].Commit != tools.Head() || byName["tools"].URL != tools.Bare || byName["platform"].Name == "" {
		t.Errorf("a runner is told to fetch each library at its commit: %+v", d.Source.Libraries)
	}
	if s := stepByLibrary(d, "tools"); s == "" {
		t.Errorf("no step reads the required library's files")
	}
}

func stepByLibrary(d *engine.RunDetail, lib string) string {
	for _, s := range d.StepList {
		if strings.HasPrefix(s.ID, "ci/lint") {
			if st := d.Graph.Step(s.ID); st != nil && st.Library == lib {
				return s.ID
			}
		}
	}
	return ""
}

func TestServerRefusesARequirementThatIsABranchOrUnknown(t *testing.T) {
	m, _, _ := requireSetup(t, "requires:\n  tools: { repo: shared-tools, version: v9 }\n")
	_, err := m.StartRun(context.Background(), "app", "main", "dana", nil)
	if err == nil || !strings.Contains(err.Error(), "has no tag or commit called \"v9\"") {
		t.Errorf("%v", err)
	}
	m3, tools3, _ := requireSetup(t, "requires:\n  tools: { repo: shared-tools, version: stable }\n")
	tools3.Branch("stable", tools3.Head())
	if _, err := m3.StartRun(context.Background(), "app", "main", "dana", nil); err == nil || !strings.Contains(err.Error(), "is a branch of shared-tools, which moves") {
		t.Errorf("a pin that is a branch: %v", err)
	}
	m2, _, _ := requireSetup(t, "requires:\n  tools: { repo: other, version: v1 }\n")
	if _, err := m2.StartRun(context.Background(), "app", "main", "dana", nil); err == nil || !strings.Contains(err.Error(), "not a library this server allows") {
		t.Errorf("%v", err)
	}
}
