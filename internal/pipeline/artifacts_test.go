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

// Where artifacts meet loops, run inputs, conditions on upstream steps and
// deploy steps.
func TestArtifactsWithOtherFeatures(t *testing.T) {
	head := "pipeline: app\ninputs:\n  dir: { type: string, default: out }\nsteps:\n"
	for _, tc := range []struct {
		name, steps, want string
		line              int
	}{
		{"fetch from a loop",
			"  test:\n    for_each: { py: [\"3.12\", \"3.13\"] }\n    image: x\n    run: y\n    artifacts: [{name: cov, paths: [c.xml]}]\n" +
				"  after:\n    image: x\n    run: y\n    needs: test\n    fetch: [{from: test, name: cov}]\n",
			"say where the members' files go with into", 14},
		{"a loop value in a name",
			"  test:\n    for_each: { py: [\"3.12\"] }\n    image: x\n    run: y\n    artifacts: [{name: {each: py}, paths: [c.xml]}]\n",
			"artifact names and paths are written as text", 9},
		{"a loop value in a path",
			"  test:\n    for_each: { py: [\"3.12\"] }\n    image: x\n    run: y\n    artifacts: [{name: cov, paths: [{each: py}]}]\n",
			"artifact names and paths are written as text", 9},
		{"an input in a path",
			"  b:\n    image: x\n    run: y\n    artifacts: [{name: out, paths: {input: dir}}]\n",
			"{ input: … } and { each: … } are not allowed here", 8},
		{"an input in into",
			"  b:\n    image: x\n    run: y\n    artifacts: [{name: out, paths: [o]}]\n" +
				"  c:\n    image: x\n    run: y\n    needs: b\n    fetch: [{from: b, name: out, into: {input: dir}}]\n",
			"not allowed here", 13},
		{"a cleanup step fetching what is kept only on success",
			"  b:\n    image: x\n    run: y\n    artifacts: [{name: out, paths: [o]}]\n" +
				"  c:\n    image: x\n    run: y\n    needs: b\n    when: { upstream: failed }\n    fetch: [{from: b, name: out}]\n",
			"keeps out only when it succeeds; keep it with when: failure", 14},
		{"artifacts on a deploy step",
			"  ship:\n    kind: deploy\n    environment: dev\n    service: web\n    version: \"1\"\n    artifacts: [{name: out, paths: [o]}]\n",
			"artifacts", 10},
		{"fetch on a rollback step",
			"  b:\n    image: x\n    run: y\n    artifacts: [{name: out, paths: [o]}]\n" +
				"  back:\n    kind: rollback\n    needs: b\n    environment: dev\n    service: web\n    fetch: [{from: b, name: out}]\n",
			"fetch", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(head + tc.steps))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if tc.line > 0 {
				ps, _ := err.(Problems)
				found := false
				for _, p := range ps {
					if strings.Contains(p.Message, tc.want) && p.Line == tc.line {
						found = true
					}
				}
				if !found {
					t.Fatalf("no problem on line %d: %v", tc.line, ps)
				}
			}
		})
	}
	// Allowed: a loop whose members each keep an artifact of the same
	// name, a looped step fetching from a step that is not repeated, and a
	// cleanup step fetching what a failed step keeps on failure.
	p, err := Parse([]byte(head + `  build:
    image: x
    run: y
    artifacts: [{name: bin, paths: [bin], when: always}]
  test:
    for_each: { py: ["3.12", "3.13"] }
    image: { each: py, format: "python:%s" }
    run: y
    needs: build
    fetch: [{from: build, name: bin}]
    artifacts: [{name: coverage, paths: [cov.xml]}]
  report:
    image: x
    run: y
    needs: build
    when: { upstream: failed }
    fetch: [{from: build, name: bin}]
`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Bind(p, nil, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range b.Steps {
		if strings.HasPrefix(s.ID, "test[") {
			n++
			if len(s.Artifacts) != 1 || s.Artifacts[0].Name != "coverage" || len(s.Fetch) != 1 || s.Fetch[0].From != "build" {
				t.Fatalf("member %s: %+v %+v", s.ID, s.Artifacts, s.Fetch)
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d members", n)
	}
}
