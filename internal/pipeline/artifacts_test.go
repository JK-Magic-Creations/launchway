package pipeline

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const artifactsPipeline = `pipeline: app
steps:
  build:
    image: golang:1.27
    run: make dist
    artifacts:
      - name: binary
        paths: [bin/app, "dist/*.tar.gz", ./bin/app]
        retain: 30d
      - name: screenshots
        paths: shots/**/*.png
        when: failure
    reports:
      findings: reports/*.sarif
      sbom: [reports/sbom.cdx.json]
  test:
    image: alpine:3.22
    needs: build
    run: ./bin/app --version
  deploy:
    image: alpine:3.22
    needs: [test]
    fetch:
      - from: build
        name: binary
        into: ./out/
      - { from: build, name: sbom }
    run: ls out
`

func TestArtifactsAndFetch(t *testing.T) {
	p, err := Parse([]byte(artifactsPipeline))
	if err != nil {
		t.Fatal(err)
	}
	b := p.Step("build")
	if len(b.Artifacts) != 2 {
		t.Fatalf("artifacts %+v", b.Artifacts)
	}
	bin := b.Artifacts[0]
	if bin.Name != "binary" || !slices.Equal(bin.Paths, []string{"bin/app", "dist/*.tar.gz"}) || bin.Retain.D() != 30*24*time.Hour || bin.When != "" {
		t.Fatalf("binary %+v", bin)
	}
	if s := b.Artifacts[1]; s.When != ArtifactOnFailure || !slices.Equal(s.Paths, []string{"shots/**/*.png"}) || s.Retain != 0 {
		t.Fatalf("screenshots %+v", s)
	}
	if !slices.Equal(b.Reports.Findings, []string{"reports/*.sarif"}) || !slices.Equal(b.Reports.SBOM, []string{"reports/sbom.cdx.json"}) || b.Reports.Tests != nil {
		t.Fatalf("reports %+v", b.Reports)
	}
	d := p.Step("deploy")
	want := []*Fetch{{From: "build", Name: "binary", Into: "out"}, {From: "build", Name: "sbom"}}
	for i := range d.Fetch {
		d.Fetch[i].Line = 0
	}
	if !reflect.DeepEqual(d.Fetch, want) {
		t.Fatalf("fetch %+v", d.Fetch)
	}
	if !p.UsesSchema12() {
		t.Fatal("a pipeline with artifacts does not say it needs schema 12")
	}
	if q, _ := Parse([]byte("pipeline: a\nsteps:\n  a: {image: x, run: y, reports: {tests: r.xml}}\n")); q.UsesSchema12() {
		t.Fatal("a pipeline with only test reports says it needs schema 12")
	}
	// The graph keeps them, and reads them back.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(p)), &back); err != nil {
		t.Fatal(err)
	}
	if back.Step("build").Artifacts[0].Retain != bin.Retain || back.Step("deploy").Fetch[0].Into != "out" ||
		!slices.Equal(back.Step("build").Reports.SBOM, b.Reports.SBOM) {
		t.Fatalf("round trip: %s", mustJSON(&back))
	}
}

func TestKeepAfter(t *testing.T) {
	for _, tc := range []struct {
		when    string
		outcome string
		keep    bool
	}{
		{"", "succeeded", true}, {"", "failed", false}, {"", "canceled", false},
		{ArtifactOnFailure, "failed", true}, {ArtifactOnFailure, "succeeded", false}, {ArtifactOnFailure, "canceled", false},
		{ArtifactAlways, "succeeded", true}, {ArtifactAlways, "failed", true}, {ArtifactAlways, "canceled", true}, {ArtifactAlways, "lost", false},
	} {
		if got := (&Artifact{When: tc.when}).KeepAfter(tc.outcome); got != tc.keep {
			t.Errorf("when %q after %s: %v", tc.when, tc.outcome, got)
		}
	}
}

func TestArtifactProblems(t *testing.T) {
	base := func(extra string) string {
		return "pipeline: app\nsteps:\n  build:\n    image: x\n    run: y\n" + extra
	}
	cases := []struct {
		name, src, want string
	}{
		{"not a list", base("    artifacts: {name: a, paths: x}\n"), "artifacts is a list"},
		{"empty list", base("    artifacts: []\n"), "list at least one artifact"},
		{"no name", base("    artifacts: [{paths: x}]\n"), "an artifact needs a name"},
		{"bad name", base("    artifacts: [{name: Bin, paths: x}]\n"), `"Bin" is not a valid artifact name`},
		{"no paths", base("    artifacts: [{name: a}]\n"), "say which files to keep"},
		{"absolute path", base("    artifacts: [{name: a, paths: /etc/passwd}]\n"), "is not relative to the workspace"},
		{"dot dot", base("    artifacts: [{name: a, paths: ../x}]\n"), "leaves the workspace (..); artifact files must be inside it"},
		{"retain words", base("    artifacts: [{name: a, paths: x, retain: forever}]\n"), "such as 30d, 2w or 12h"},
		{"retain short", base("    artifacts: [{name: a, paths: x, retain: 5m}]\n"), "between 1h and 3650d"},
		{"when", base("    artifacts: [{name: a, paths: x, when: sometimes}]\n"), "when is success, failure or always"},
		{"twice", base("    artifacts: [{name: a, paths: x}, {name: a, paths: y}]\n"), "artifact a is listed twice"},
		{"sbom taken", base("    reports: {sbom: s.json}\n    artifacts: [{name: sbom, paths: x}]\n"), "the artifact name sbom is taken"},
		{"unknown key", base("    artifacts: [{name: a, paths: x, keep: 1}]\n"), `unknown key "keep"`},
		{"approval", "pipeline: app\nsteps:\n  ok:\n    kind: approval\n    artifacts: [{name: a, paths: x}]\n", "artifacts"},
		{"fetch self", base("    artifacts: [{name: a, paths: x}]\n    fetch: [{from: build, name: a}]\n"), "cannot fetch its own"},
		{"fetch unknown step", base("    fetch: [{from: nope, name: a}]\n"), `fetches from "nope", but there is no step`},
		{"fetch not needed", base("    artifacts: [{name: a, paths: x}]\n  later:\n    image: x\n    run: y\n    fetch: [{from: build, name: a}]\n"),
			"which this step does not need"},
		{"fetch unknown artifact", base("    artifacts: [{name: a, paths: x}]\n  later:\n    image: x\n    run: y\n    needs: build\n    fetch: [{from: build, name: b}]\n"),
			`build keeps no artifact called "b"`},
		{"fetch twice", base("    artifacts: [{name: a, paths: x}]\n  later:\n    image: x\n    run: y\n    needs: build\n    fetch: [{from: build, name: a}, {from: build, name: a}]\n"),
			"fetched twice"},
		{"fetch into up", base("    artifacts: [{name: a, paths: x}]\n  later:\n    image: x\n    run: y\n    needs: build\n    fetch: [{from: build, name: a, into: ../x}]\n"),
			"leaves the workspace"},
		{"fetch into abs", base("    artifacts: [{name: a, paths: x}]\n  later:\n    image: x\n    run: y\n    needs: build\n    fetch: [{from: build, name: a, into: /tmp}]\n"),
			"not relative to the workspace"},
		{"fetch into git", base("    artifacts: [{name: a, paths: x}]\n  later:\n    image: x\n    run: y\n    needs: build\n    fetch: [{from: build, name: a, into: .git/hooks}]\n"),
			"inside .git"},
		{"fetch missing name", base("    fetch: [{from: build}]\n"), "say which artifact"},
		{"findings empty", base("    reports: {findings: []}\n"), "list at least one file or glob, for example: findings:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if err == nil {
				t.Fatalf("parsed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFetchNeedsThroughOtherSteps(t *testing.T) {
	p, err := Parse([]byte(`pipeline: app
steps:
  build: {image: x, run: y, artifacts: [{name: a, paths: x}]}
  test: {image: x, run: y, needs: build}
  ship: {image: x, run: y, needs: test, fetch: [{from: build, name: a}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if f := p.Step("ship").Fetch; len(f) != 1 || f[0].From != "build" {
		t.Fatalf("fetch %+v", f)
	}
}

func TestParseRetain(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 720 * time.Hour, "2w": 336 * time.Hour, "12h": 12 * time.Hour, "90m": 90 * time.Minute} {
		if d, ok := ParseRetain(in); !ok || d != want {
			t.Errorf("%s: %v %v", in, d, ok)
		}
	}
	for _, in := range []string{"", "d", "-1d", "1.5d", "x", "99999999d"} {
		if _, ok := ParseRetain(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestFetchInTemplates(t *testing.T) {
	tmpl := `template: build
steps:
  compile: {image: x, run: y, artifacts: [{name: bin, paths: [out/app]}]}
  check: {image: x, run: y, needs: compile, fetch: [{from: compile, name: bin, into: got}]}
`
	src := `pipeline: app
libraries:
  platform: { repo: ci-templates, version: v3 }
steps:
  b:
    uses: platform/build
`
	p := mustExpand(t, src, map[string]string{"build": tmpl})
	f := p.Step("b/check").Fetch
	if len(f) != 1 || f[0].From != "b/compile" || f[0].Name != "bin" || f[0].Into != "got" {
		t.Fatalf("fetch %+v", f)
	}
	if a := p.Step("b/compile").Artifacts; len(a) != 1 || a[0].Name != "bin" {
		t.Fatalf("artifacts %+v", a)
	}
	// A template's step whose source step is left out by its if.
	tmpl2 := `template: build
params:
  compile: { type: boolean, default: false }
steps:
  compile: {image: x, run: y, if: { param: compile }, artifacts: [{name: bin, paths: [out/app]}]}
  check: {image: x, run: y, needs: compile, fetch: [{from: compile, name: bin}]}
`
	_, err := expand(t, src, map[string]string{"build": tmpl2})
	findProblem(t, problemsOf(t, err), "which the template leaves out")
	// From a pipeline, fetching from a uses step is refused for now.
	_, err = Parse([]byte(src + "  after:\n    image: x\n    run: y\n    needs: b\n    fetch: [{from: b, name: bin}]\n"))
	if err == nil || !strings.Contains(err.Error(), "uses a template; fetching an artifact a template's step keeps is not supported yet") {
		t.Fatalf("got %v", err)
	}
}
