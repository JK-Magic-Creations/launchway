package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/starters"
)

const (
	reqCommitLib   = "1111111111111111111111111111111111111111"
	reqCommitTools = "2222222222222222222222222222222222222222"
)

// expandRequiring expands a pipeline that uses the library lib, which requires tools, with the templates of each
// library as the maps give them; the starters come from the built-in library.
func expandRequiring(t *testing.T, lib, tools map[string]string, tweak func(lib, tools *Library)) (*Pipeline, error) {
	t.Helper()
	p, err := Parse([]byte("pipeline: p\nlibraries:\n  platform: { repo: ci, version: v1 }\nsteps:\n  a:\n    uses: platform/outer\n"))
	if err != nil {
		t.Fatal(err)
	}
	l := p.Library("platform")
	l.Commit = reqCommitLib
	dep := &Library{Name: "tools", Repo: "shared-tools", Version: "v2.1.0", Commit: reqCommitTools, RequiredBy: "platform"}
	l.Requires = []*Library{dep}
	if tweak != nil {
		tweak(l, dep)
	}
	return Expand(p, func(l *Library, name string) ([]byte, error) {
		var src map[string]string
		switch l.Repo {
		case "ci":
			src = lib
		case "shared-tools":
			src = tools
		case starters.Name:
			if b, ok := starters.Template(name); ok {
				return b, nil
			}
			return nil, ErrNoTemplate
		}
		if s, ok := src[name]; ok {
			return []byte(s), nil
		}
		return nil, ErrNoTemplate
	})
}

const (
	outerUsingBoth = `template: outer
steps:
  lint: { uses: tools/lint }
  secrets: { uses: launchway/secret-scan, needs: lint }
`
	toolsLint = `template: lint
steps:
  run: { image: alpine:3.22, run: "$LAUNCHWAY_LIB/scripts/lint.sh" }
`
)

// A library's template uses the starters and a template of a library it requires; the libraries it asked for are
// listed with the pipeline's own, with what asked for each, and each step knows the library its files are in.
func TestLibraryTemplatesUseStartersAndRequiredLibraries(t *testing.T) {
	p, err := expandRequiring(t, map[string]string{"outer": outerUsingBoth}, map[string]string{"lint": toolsLint}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Step("a/lint/run"); s == nil || s.Library != "tools" {
		t.Fatalf("the required library's step must find its own scripts: %+v", s)
	}
	if s := p.Step("a/secrets/run"); s == nil || s.Library != "launchway" {
		t.Fatalf("a starter's step: %+v", s)
	}
	got := map[string]*Library{}
	for _, l := range p.Libraries {
		got[l.Name] = l
	}
	if got["platform"] == nil || got["tools"] == nil || got["launchway"] == nil {
		t.Fatalf("libraries: %v", p.Libraries)
	}
	if tl := got["tools"]; tl.RequiredBy != "platform" || tl.Commit != reqCommitTools || tl.Version != "v2.1.0" {
		t.Errorf("the resolved requirement is listed with its pin and commit: %+v", tl)
	}
	if got["launchway"].RefKind != "builtin" || got["launchway"].Commit != starters.Commit() {
		t.Errorf("%+v", got["launchway"])
	}
}

func TestLibraryTemplatesCannotUseWhatTheirLibraryDoesNotRequire(t *testing.T) {
	_, err := expandRequiring(t, map[string]string{"outer": strings.Replace(outerUsingBoth, "tools/lint", "elsewhere/lint", 1)}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `has no requirement called "elsewhere" (it requires tools)`) {
		t.Errorf("%v", err)
	}
	_, err = expandRequiring(t, map[string]string{"outer": strings.Replace(outerUsingBoth, "launchway/secret-scan", "launchway/no-such", 1)}, map[string]string{"lint": toolsLint}, nil)
	if err == nil || !strings.Contains(err.Error(), "no template") {
		t.Errorf("a starter that does not exist is named: %v", err)
	}
}

// Libraries that use each other's templates in a circle are found, and each library's templates are told apart.
func TestRequiredLibrariesMayNotUseEachOtherInACircle(t *testing.T) {
	toolsBack := `template: lint
steps:
  back: { uses: platform/outer }
`
	_, err := expandRequiring(t, map[string]string{"outer": outerUsingBoth}, map[string]string{"lint": toolsBack}, func(lib, tools *Library) {
		back := *lib
		back.Name, back.Requires = "platform", nil
		tools.Requires = []*Library{&back}
	})
	if err == nil || !strings.Contains(err.Error(), "templates use each other in a loop") {
		t.Errorf("%v", err)
	}
}

func TestLibraryManifest(t *testing.T) {
	reqs, ps := ParseLibraryManifest([]byte("requires:\n  tools: { repo: shared-tools, version: v2.1.0 }\n  more: { repo: other, version: 0123456789012345678901234567890123456789 }\n"))
	if len(ps) > 0 || len(reqs) != 2 || reqs[0].Name != "tools" || reqs[0].Repo != "shared-tools" || reqs[0].Version != "v2.1.0" {
		t.Fatalf("%v %v", reqs, ps)
	}
	for text, want := range map[string]string{
		"requires:\n  tools: { repo: x, version: main }\n":   "reads as a branch",
		"requires:\n  tools: { repo: x }\n":                  "pin the version",
		"requires:\n  tools: { version: v1 }\n":              "name the library",
		"requires:\n  launchway: { repo: x, version: v1 }\n": "starters that ship with Launchway",
		"required: {}\n": "unknown key",
	} {
		if _, ps := ParseLibraryManifest([]byte(text)); len(ps) == 0 || !strings.Contains(ps.Error(), want) {
			t.Errorf("%q: %v", text, ps)
		}
	}
}

// Offline, a library that requires another is expanded from the directories given for both, validate --resolved
// lists the required one with what asked for it, and a missing directory or a circle is named.
func TestOfflineExpansionFollowsRequirements(t *testing.T) {
	write := func(dir, name, text string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	platform, tools := t.TempDir(), t.TempDir()
	write(platform, "templates/outer.yaml", outerUsingBoth)
	write(platform, LibraryManifestPath, "requires:\n  tools: { repo: shared-tools, version: v2.1.0 }\n")
	write(tools, "templates/lint.yaml", toolsLint)
	src := []byte("pipeline: p\nlibraries:\n  platform: { repo: ci, version: v1 }\nsteps:\n  a:\n    uses: platform/outer\n")
	p, err := ExpandOffline(src, map[string]string{"platform": platform, "tools": tools}, BindOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range p.Libraries {
		names = append(names, l.Name+":"+l.RequiredBy)
	}
	if strings.Join(names, " ") != "platform: tools:platform launchway:" {
		t.Errorf("libraries: %v", names)
	}
	if s := p.Step("a/lint/run"); s == nil || s.Library != "tools" {
		t.Errorf("%+v", s)
	}
	if _, err := ExpandOffline(src, map[string]string{"platform": platform}, BindOptions{}, nil); err == nil || !strings.Contains(err.Error(), "--library tools=DIRECTORY") {
		t.Errorf("a required library with no directory: %v", err)
	}
	write(tools, LibraryManifestPath, "requires:\n  platform: { repo: ci, version: v1 }\n")
	if _, err := ExpandOffline(src, map[string]string{"platform": platform, "tools": tools}, BindOptions{}, nil); err == nil || !strings.Contains(err.Error(), "circle") {
		t.Errorf("a circle: %v", err)
	}
}
