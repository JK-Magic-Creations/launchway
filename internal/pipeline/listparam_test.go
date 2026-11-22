package pipeline

import (
	"strings"
	"testing"
)

// A parameter of type list is a list of patterns wherever a list of patterns is accepted, not one pattern that reads
// ["feat/*","fix/*"].
func TestListParametersExpandToListsOfPatterns(t *testing.T) {
	const tmpl = `template: lp
params:
  branches: { type: list, default: [main] }
  files: { type: list, default: ["src/*.go"] }
  globs: { type: list, default: ["a/*.xml"] }
steps:
  build:
    image: alpine:3.22
    run: make
    when: { branch: { param: branches }, changed: { param: files } }
    reports: { tests: { param: globs } }
    artifacts: [ { name: out, paths: { param: files } } ]
    cache: [ { name: c, paths: { param: files }, key: { files: { param: globs } } } ]
  other:
    image: alpine:3.22
    run: make
    needs: build
    when: { branch: [ release, { param: branches } ] }
`
	p := mustExpand(t, "pipeline: p\nlibraries:\n  platform: { repo: shared, version: v1 }\nsteps:\n  ci:\n    uses: platform/lp\n    with:\n      branches: [\"feat/*\", \"fix/*\"]\n      files: [src/*.go, go.mod]\n      globs: [a/*.xml, b/*.xml]\n",
		map[string]string{"lp": tmpl})
	b := p.Step("ci/build")
	if got := strings.Join(b.When.Branch, " "); got != "feat/* fix/*" {
		t.Errorf("when.branch: %q", got)
	}
	if got := strings.Join(b.When.Changed, " "); got != "src/*.go go.mod" {
		t.Errorf("when.changed: %q", got)
	}
	if b.Reports == nil || strings.Join(b.Reports.Tests, " ") != "a/*.xml b/*.xml" {
		t.Errorf("reports.tests: %+v", b.Reports)
	}
	if len(b.Artifacts) != 1 || strings.Join(b.Artifacts[0].Paths, " ") != "src/*.go go.mod" {
		t.Errorf("artifact paths: %+v", b.Artifacts)
	}
	if len(b.Cache) != 1 || strings.Join(b.Cache[0].Paths, " ") != "src/*.go go.mod" {
		t.Errorf("cache paths: %+v", b.Cache)
	}
	if got := strings.Join(p.Step("ci/other").When.Branch, " "); got != "release feat/* fix/*" {
		t.Errorf("a list parameter inside a list: %q", got)
	}
	// One value still works as it did.
	p = mustExpand(t, "pipeline: p\nlibraries:\n  platform: { repo: shared, version: v1 }\nsteps:\n  ci:\n    uses: platform/lp\n", map[string]string{"lp": tmpl})
	if got := strings.Join(p.Step("ci/build").When.Branch, " "); got != "main" {
		t.Errorf("%q", got)
	}
}
