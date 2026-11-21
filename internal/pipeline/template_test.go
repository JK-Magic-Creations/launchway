package pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

// expand parses a pipeline, pretends every library resolved to testCommit,
// and expands it with templates from the map (name → text).
func expand(t *testing.T, src string, templates map[string]string) (*Pipeline, error) {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, l := range p.Libraries {
		l.Commit = testCommit
	}
	return Expand(p, func(lib *Library, name string) ([]byte, error) {
		if lib.Commit != testCommit {
			t.Fatalf("loaded %s from %s at %q", name, lib.Name, lib.Commit)
		}
		text, ok := templates[name]
		if !ok {
			return nil, ErrNoTemplate
		}
		return []byte(text), nil
	})
}

func mustExpand(t *testing.T, src string, templates map[string]string) *Pipeline {
	t.Helper()
	p, err := expand(t, src, templates)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	return p
}

func problemsOf(t *testing.T, err error) Problems {
	t.Helper()
	var ps Problems
	if !errors.As(err, &ps) {
		t.Fatalf("want problems, got %T %v", err, err)
	}
	return ps
}

// findProblem returns the problem whose message contains want, or fails.
func findProblem(t *testing.T, ps Problems, want string) Problem {
	t.Helper()
	for _, p := range ps {
		if strings.Contains(p.Message, want) {
			return p
		}
	}
	t.Fatalf("no problem says %q; got:\n%v", want, ps)
	return Problem{}
}

const pythonService = `template: python-service
description: Lint, test and scan a Python service
params:
  test_command: { type: string,  default: "python3 -m pytest -q", description: How to run the tests }
  scan:         { type: boolean, default: true }
  python:       { type: string,  default: "3.13", options: ["3.12", "3.13"] }
containers:
  py: { image: { param: python, format: "docker.io/library/python:%s-slim" } }
steps:
  lint:  { container: py, run: "$LAUNCHWAY_LIB/scripts/lint.sh" }
  test:  { container: py, needs: lint, run: "$PARAM_TEST_COMMAND" }
  scan:  { container: py, needs: lint, if: { param: scan }, run: "$LAUNCHWAY_LIB/scripts/scan.sh" }
outputs:
  coverage: { from: test.coverage }
`

const usesPython = `pipeline: mcp-weather
libraries:
  platform: { repo: ci-templates, version: v3 }
steps:
  prepare:
    image: alpine:3.22
    run: echo prepare
  ci:
    uses: platform/python-service
    needs: prepare
    with: { test_command: "pytest -q tests/" }
  deploy:
    image: alpine:3.22
    needs: ci
    run: echo "coverage was $LAUNCHWAY_NEEDS_CI_COVERAGE"
`

func TestExpandIDsGroupsNeedsAndOutputs(t *testing.T) {
	p := mustExpand(t, usesPython, map[string]string{"python-service": pythonService})
	var ids []string
	for _, s := range p.Steps {
		ids = append(ids, s.ID)
	}
	if want := []string{"prepare", "ci/lint", "ci/test", "ci/scan", "deploy"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("steps %v, want %v", ids, want)
	}
	// The first template step needs nothing in the template, so it needs
	// what the uses step needs.
	if got := p.Step("ci/lint").Needs; !reflect.DeepEqual(got, []string{"prepare"}) {
		t.Fatalf("ci/lint needs %v", got)
	}
	if got := p.Step("ci/test").Needs; !reflect.DeepEqual(got, []string{"ci/lint"}) {
		t.Fatalf("ci/test needs %v", got)
	}
	// Fan-in: what needs the uses step needs every step it became.
	d := p.Step("deploy")
	if !reflect.DeepEqual(d.Needs, []string{"ci/lint", "ci/test", "ci/scan"}) || !reflect.DeepEqual(d.NeedsGroups, []string{"ci"}) {
		t.Fatalf("deploy needs %v groups %v", d.Needs, d.NeedsGroups)
	}
	for _, id := range []string{"ci/lint", "ci/test", "ci/scan"} {
		s := p.Step(id)
		if s.Group != "ci" || s.Library != "platform" {
			t.Fatalf("%s: group %q library %q", id, s.Group, s.Library)
		}
		if s.Uses != "" || s.With != nil {
			t.Fatalf("%s still says uses", id)
		}
	}
	if p.Step("prepare").Group != "" || p.Step("prepare").Library != "" {
		t.Fatal("a pipeline step got a group")
	}
	g := p.Group("ci")
	if g == nil || g.Uses != "platform/python-service" || g.Library != "platform" || g.Template != "python-service" ||
		g.Description != "Lint, test and scan a Python service" || !reflect.DeepEqual(g.Steps, []string{"ci/lint", "ci/test", "ci/scan"}) {
		t.Fatalf("group %+v", g)
	}
	if want := map[string]string{"test_command": "pytest -q tests/", "scan": "true", "python": "3.13"}; !reflect.DeepEqual(g.Params, want) {
		t.Fatalf("params %v", g.Params)
	}
	if want := (OutputRef{Step: "ci/test", Key: "coverage"}); g.Outputs["coverage"] != want {
		t.Fatalf("outputs %v", g.Outputs)
	}
	// The template's container, under a name of the run's.
	c := p.Container("ci-py")
	if c == nil || c.Image != "docker.io/library/python:3.13-slim" {
		t.Fatalf("containers %+v", p.Containers)
	}
	if s := p.Step("ci/test"); s.Container != "ci-py" || s.Image != c.Image {
		t.Fatalf("ci/test runs in %q %q", s.Container, s.Image)
	}
	// Every template step receives every parameter.
	env := p.Step("ci/lint").Env
	if env["PARAM_TEST_COMMAND"] != "pytest -q tests/" || env["PARAM_SCAN"] != "true" || env["PARAM_PYTHON"] != "3.13" {
		t.Fatalf("env %v", env)
	}
	if len(p.Step("deploy").Env) != 0 {
		t.Fatalf("a pipeline step got parameters: %v", p.Step("deploy").Env)
	}
	if len(p.Libraries) != 1 || p.Libraries[0].Commit != testCommit {
		t.Fatalf("libraries %+v", p.Libraries)
	}
	if !p.Expanded() {
		t.Fatal("not expanded")
	}
	// The graph's levels still put each step after what it needs.
	levels := p.Levels()
	if want := [][]string{{"prepare"}, {"ci/lint"}, {"ci/test", "ci/scan"}, {"deploy"}}; !reflect.DeepEqual(levels, want) {
		t.Fatalf("levels %v", levels)
	}
}

func TestExpandParameterUses(t *testing.T) {
	tmpl := `template: all-uses
params:
  image:   { type: string, default: alpine }
  tag:     { type: string, default: "3.22" }
  retries: { type: number, default: "2" }
  limit:   { type: string, default: 5m }
  branch:  { type: string, default: main }
  level:   { type: string, default: low, options: [low, high] }
  deploy:  { type: boolean, default: false }
steps:
  a:
    image: { param: tag, format: "docker.io/library/alpine:%s" }
    env:
      WHERE: { param: branch }
      PLAIN: fixed
      IMG:   { param: image, format: "img=%s" }
    timeout: { param: limit }
    retry: { param: retries }
    when: { branch: [{ param: branch }, "release/*"], event: { param: level, format: "%s" } }
    run: echo
  b:
    image: { param: image }
    if: { param: level, equals: high }
    run: echo high
  c:
    image: { param: image }
    if: { param: deploy }
    run: echo deploy
`
	src := `pipeline: app
libraries:
  lib: { repo: r, version: v1 }
steps:
  t:
    uses: lib/all-uses
    with: { branch: develop, level: push, retries: 3, limit: 90s }
`
	// level: push is not one of its options.
	_, err := expand(t, src, map[string]string{"all-uses": tmpl})
	pr := findProblem(t, problemsOf(t, err), `level must be one of "low", "high"; "push" is not`)
	if pr.Line != 7 || pr.File != "" {
		t.Fatalf("problem at %d in %q", pr.Line, pr.File)
	}

	src = strings.Replace(src, "level: push", "level: high", 1)
	_, err = expand(t, src, map[string]string{"all-uses": tmpl})
	// event: high is not an event: the value reached the when list and was checked there.
	pr = findProblem(t, problemsOf(t, err), `event "high" is not known`)
	if pr.File != "templates/all-uses.yaml" || pr.Library != "lib" || pr.Line != 19 {
		t.Fatalf("problem %+v", pr)
	}

	tmpl = strings.Replace(tmpl, `event: { param: level, format: "%s" }`, `tag: { param: level, format: "v-%s" }`, 1)
	p := mustExpand(t, src, map[string]string{"all-uses": tmpl})
	a := p.Step("t/a")
	if a.Image != "docker.io/library/alpine:3.22" || a.Env["WHERE"] != "develop" || a.Env["PLAIN"] != "fixed" || a.Env["IMG"] != "img=alpine" {
		t.Fatalf("a: image %q env %v", a.Image, a.Env)
	}
	if a.Timeout.Short() != "1m30s" || a.Retry != 3 {
		t.Fatalf("a: timeout %s retry %d", a.Timeout.Short(), a.Retry)
	}
	if !reflect.DeepEqual(a.When.Branch, []string{"develop", "release/*"}) || !reflect.DeepEqual(a.When.Tag, []string{"v-high"}) {
		t.Fatalf("a: when %+v", a.When)
	}
	if p.Step("t/b") == nil {
		t.Fatal("b's if (level equals high) was true, but it is gone")
	}
	if p.Step("t/c") != nil {
		t.Fatal("c's if (deploy, default false) was false, but it is there")
	}
	if got := p.Group("t").Steps; !reflect.DeepEqual(got, []string{"t/a", "t/b"}) {
		t.Fatalf("group steps %v", got)
	}
	// Containers made for direct images: one per image.
	if n := len(p.RunContainers()); n != 2 {
		t.Fatalf("run containers %+v", p.RunContainers())
	}
}

func TestExpandDefaultsRequiredUnknownAndTypes(t *testing.T) {
	tmpl := `template: needs-values
params:
  name:   { type: string }
  count:  { type: number, default: "1" }
  strict: { type: boolean, default: false }
steps:
  a: { image: alpine, run: "echo $PARAM_NAME $PARAM_COUNT $PARAM_STRICT" }
`
	cases := []struct {
		with, want string
		line       int
	}{
		{`{}`, "needs a value for name, which has no default", 6},
		{`{ name: x, colour: red }`, `has no parameter "colour"; it takes name, count, strict`, 7},
		{`{ name: x, count: many }`, `count is a number; "many" is not one`, 7},
		{`{ name: x, strict: maybe }`, `strict is true or false; "maybe" is neither`, 7},
	}
	for _, c := range cases {
		src := "pipeline: app\nlibraries:\n  lib: { repo: r, version: main }\nsteps:\n  s:\n    uses: lib/needs-values\n    with: " + c.with + "\n"
		_, err := expand(t, src, map[string]string{"needs-values": tmpl})
		pr := findProblem(t, problemsOf(t, err), c.want)
		if pr.Line != c.line || pr.File != "" || !strings.HasPrefix(pr.Path, "steps.s.with.") {
			t.Fatalf("%s: problem %+v", c.with, pr)
		}
	}
	p := mustExpand(t, "pipeline: app\nlibraries:\n  lib: { repo: r, version: main }\nsteps:\n  s:\n    uses: lib/needs-values\n    with: { name: x, count: 2.5 }\n",
		map[string]string{"needs-values": tmpl})
	if got := p.Group("s").Params; !reflect.DeepEqual(got, map[string]string{"name": "x", "count": "2.5", "strict": "false"}) {
		t.Fatalf("params %v", got)
	}
}

func TestTemplateProblemsSayWhichFile(t *testing.T) {
	tmpl := `template: broken
params:
  a: { type: colour }
  b: { type: number, default: lots }
  c: { type: string, default: x, options: [y, z] }
steps:
  one:
    image: { param: nope }
    run: echo
    env: { PARAM_A: x }
  two:
    image: alpine
    needs: three
    run: echo
  four:
    image: { param: c, format: "%d" }
    run: echo
    if: { param: c }
outputs:
  out: { from: five.x }
`
	src := "pipeline: app\nlibraries:\n  lib: { repo: r, version: main }\nsteps:\n  s:\n    uses: lib/broken\n"
	_, err := expand(t, src, map[string]string{"broken": tmpl})
	ps := problemsOf(t, err)
	inTemplate := func(want string, line int) {
		t.Helper()
		pr := findProblem(t, ps, want)
		if pr.File != "templates/broken.yaml" || pr.Library != "lib" || pr.Line != line {
			t.Fatalf("%q: %+v", want, pr)
		}
	}
	inTemplate("the type is string, number, boolean, list, environment or application", 3)
	inTemplate(`the default does not fit: b is a number; "lots" is not one`, 4)
	inTemplate(`the default does not fit: c must be one of "y", "z"`, 5)
	// The pipeline file says, at the uses step, that the template has problems.
	pr := findProblem(t, ps, "the template lib/broken has 3 problems, listed with its file")
	if pr.File != "" || pr.Line != 6 {
		t.Fatalf("%+v", pr)
	}
	if !strings.Contains(ps.Error(), "templates/broken.yaml (library lib): line 3:") {
		t.Fatalf("the text does not name the file:\n%s", ps.Error())
	}

	// With the parameters fixed, the steps are read and their problems found.
	tmpl = strings.Replace(tmpl, "colour", "string, default: x", 1)
	tmpl = strings.Replace(tmpl, "default: lots", `default: "3"`, 1)
	tmpl = strings.Replace(tmpl, "options: [y, z]", "options: [x, y]", 1)
	_, err = expand(t, src, map[string]string{"broken": tmpl})
	ps = problemsOf(t, err)
	inTemplate(`there is no parameter "nope"; the template declares a, b, c`, 8)
	inTemplate("names starting with PARAM_ are the template's parameters", 10)
	inTemplate(`needs "three", but there is no step with that name`, 13)
	inTemplate("a format has exactly one %s", 16)
	inTemplate("c is a string, not true or false: say which value keeps the step", 18)
	inTemplate(`there is no step called "five"`, 20)

	// A template that is not there, and one whose name does not match.
	_, err = expand(t, src, map[string]string{})
	pr = findProblem(t, problemsOf(t, err), "the library lib has no template broken: there is no templates/broken.yaml in library lib (main at 0123456789)")
	if pr.Line != 6 || pr.File != "" {
		t.Fatalf("%+v", pr)
	}
	_, err = expand(t, src, map[string]string{"broken": "template: other\nsteps:\n  a: { image: x, run: y }\n"})
	inTemplateName := findProblem(t, problemsOf(t, err), "the file templates/broken.yaml is the template broken; it says template: other")
	if inTemplateName.Line != 1 {
		t.Fatalf("%+v", inTemplateName)
	}
}

func TestParseProblemsWithUses(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"undeclared library", "pipeline: a\nsteps:\n  s: { uses: lib/t }\n", `no library called "lib" is listed under libraries:`},
		{"uses without library", "pipeline: a\nlibraries:\n  lib: {repo: r, version: v1}\nsteps:\n  s: { uses: t }\n", "write uses: library/template"},
		{"with without uses", "pipeline: a\nsteps:\n  s: { image: x, run: y, with: {a: b} }\n", "with passes values to a template, so it goes with uses"},
		{"param outside a template", "pipeline: a\nsteps:\n  s: { image: { param: x }, run: y }\n", "{ param: … } is for templates"},
		{"if outside a template", "pipeline: a\nsteps:\n  s: { image: x, run: y, if: { param: x } }\n", `unknown key "if"`},
		{"bad version", "pipeline: a\nlibraries:\n  lib: {repo: r, version: --upload-pack=x}\nsteps:\n  s: { uses: lib/t }\n", "the version is a tag, a branch or a full commit id"},
		{"bad library name", "pipeline: a\nlibraries:\n  Lib_1: {repo: r, version: v1}\nsteps:\n  s: { uses: Lib_1/t }\n", "is not a valid library name"},
		{"missing version", "pipeline: a\nlibraries:\n  lib: {repo: r}\nsteps:\n  s: { uses: lib/t }\n", "say which version"},
		{"bad repo", "pipeline: a\nlibraries:\n  lib: {repo: \"https://evil.example/x.git\", version: v1}\nsteps:\n  s: { uses: lib/t }\n", "repo names one of the libraries the server allows"},
		{"too many with values", "pipeline: a\nlibraries:\n  lib: {repo: r, version: v1}\nsteps:\n  s: { uses: lib/t, with: {" + manyWith(51) + "} }\n", "at most 50 values"},
		{"long with value", "pipeline: a\nlibraries:\n  lib: {repo: r, version: v1}\nsteps:\n  s: { uses: lib/t, with: {k: " + strings.Repeat("x", MaxParamValueLen+1) + "} }\n", "longer than 4096 bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			findProblem(t, problemsOf(t, err), c.want)
		})
	}
	// Unexpanded, a uses step is one node of the graph.
	p, err := Parse([]byte(usesPython))
	if err != nil {
		t.Fatal(err)
	}
	if p.Expanded() || !p.Step("ci").IsUse() || p.Step("ci").With["test_command"] != "pytest -q tests/" {
		t.Fatalf("%+v", p.Step("ci"))
	}
	if n := len(p.RunContainers()); n != 1 {
		t.Fatalf("a uses step was given a container: %+v", p.RunContainers())
	}
}

func manyWith(n int) string {
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, "k"+itoa(i)+": v")
	}
	return strings.Join(parts, ", ")
}

func TestNestingWithinALibrary(t *testing.T) {
	outer := `template: outer
params:
  flavour: { type: string, default: plain }
  extra:   { type: boolean, default: true }
steps:
  setup: { image: alpine, run: echo setup }
  inner:
    uses: inner
    needs: setup
    with: { flavour: { param: flavour } }
    timeout: 5m
  extra:
    uses: inner
    if: { param: extra }
  last: { image: alpine, needs: inner, run: "echo $LAUNCHWAY_NEEDS_INNER_RESULT" }
outputs:
  result: { from: inner.result }
`
	inner := `template: inner
params:
  flavour: { type: string }
steps:
  one: { image: { param: flavour, format: "img:%s" }, run: echo one }
  two: { image: alpine, needs: one, run: echo two, timeout: 1m }
outputs:
  result: { from: two.value }
`
	src := `pipeline: app
libraries:
  lib: { repo: r, version: v2 }
steps:
  first: { image: alpine, run: echo first }
  o:
    uses: lib/outer
    needs: first
    with: { flavour: spicy, extra: false }
    retry: 2
  after: { image: alpine, needs: o, run: "echo $LAUNCHWAY_NEEDS_O_RESULT" }
`
	tm := map[string]string{"outer": outer, "inner": inner}
	p := mustExpand(t, src, tm)
	var ids []string
	for _, s := range p.Steps {
		ids = append(ids, s.ID)
	}
	if want := []string{"first", "o/setup", "o/inner/one", "o/inner/two", "o/last", "after"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("steps %v", ids)
	}
	one := p.Step("o/inner/one")
	if one.Group != "o/inner" || one.Image != "img:spicy" || !reflect.DeepEqual(one.Needs, []string{"o/setup"}) || one.Library != "lib" {
		t.Fatalf("one %+v", one)
	}
	// The nested uses step's timeout reaches one; two sets its own. The
	// pipeline's retry reaches every step.
	if one.Timeout.Short() != "5m" || p.Step("o/inner/two").Timeout.Short() != "1m" || one.Retry != 2 || p.Step("o/setup").Retry != 2 {
		t.Fatalf("timeouts %s %s retry %d", one.Timeout.Short(), p.Step("o/inner/two").Timeout.Short(), one.Retry)
	}
	if got := p.Step("o/setup").Needs; !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("setup needs %v", got)
	}
	last := p.Step("o/last")
	if !reflect.DeepEqual(last.Needs, []string{"o/inner/one", "o/inner/two"}) || !reflect.DeepEqual(last.NeedsGroups, []string{"o/inner"}) {
		t.Fatalf("last %v %v", last.Needs, last.NeedsGroups)
	}
	after := p.Step("after")
	if !reflect.DeepEqual(after.Needs, []string{"o/setup", "o/inner/one", "o/inner/two", "o/last"}) {
		t.Fatalf("after needs %v", after.Needs)
	}
	og, ig := p.Group("o"), p.Group("o/inner")
	if og == nil || ig == nil || ig.Parent != "o" || og.Parent != "" || ig.Template != "inner" {
		t.Fatalf("groups %+v %+v", og, ig)
	}
	if want := (OutputRef{Step: "o/inner/two", Key: "value"}); og.Outputs["result"] != want || ig.Outputs["result"] != want {
		t.Fatalf("outputs %v %v", og.Outputs, ig.Outputs)
	}
	if p.Group("o/extra") != nil {
		t.Fatal("a nested uses step whose if was false was expanded")
	}

	// Nested templates must be of the same library.
	tm["outer"] = strings.Replace(outer, "uses: inner\n    needs", "uses: other/inner\n    needs", 1)
	_, err := expand(t, src, tm)
	pr := findProblem(t, problemsOf(t, err), "a template can use templates of its own library only")
	if pr.File != "templates/outer.yaml" || pr.Line != 8 {
		t.Fatalf("%+v", pr)
	}

	// Templates that use each other.
	tm["outer"] = outer
	tm["inner"] = strings.Replace(inner, "  two:", "  loop: { uses: outer, with: { extra: false } }\n  two:", 1)
	_, err = expand(t, src, tm)
	pr = findProblem(t, problemsOf(t, err), "templates use each other in a loop: outer → inner → outer")
	if pr.File != "templates/inner.yaml" {
		t.Fatalf("%+v", pr)
	}

	// A with value in a template that does not fit the nested template.
	tm["inner"] = strings.Replace(inner, "flavour: { type: string }", "flavour: { type: string, options: [mild] }", 1)
	_, err = expand(t, src, tm)
	pr = findProblem(t, problemsOf(t, err), `flavour must be one of "mild"; "spicy" is not`)
	if pr.File != "templates/outer.yaml" || pr.Line != 10 || pr.Path != "steps.inner.with.flavour" {
		t.Fatalf("%+v", pr)
	}
}

func TestExpandLeavingStepsOut(t *testing.T) {
	tmpl := `template: optional
params:
  build: { type: boolean, default: false }
steps:
  build: { image: alpine, run: echo build, if: { param: build } }
  test:  { image: alpine, needs: build, run: echo test }
`
	src := "pipeline: app\nlibraries:\n  lib: { repo: r, version: v1 }\nsteps:\n  a: { image: alpine, run: echo a }\n  t: { uses: lib/optional, needs: a }\n  z: { image: alpine, needs: t, run: echo z }\n"
	p := mustExpand(t, src, map[string]string{"optional": tmpl})
	// test needed build, which was left out, so it needs what build would have.
	if got := p.Step("t/test").Needs; !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("test needs %v", got)
	}
	if p.Step("t/build") != nil {
		t.Fatal("build is there")
	}

	// A template left with no steps stands aside too.
	tmpl = strings.Replace(tmpl, "  test:  { image: alpine, needs: build, run: echo test }\n", "", 1)
	p = mustExpand(t, src, map[string]string{"optional": tmpl})
	if got := p.Step("z").Needs; !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("z needs %v", got)
	}
	if g := p.Group("t"); g == nil || len(g.Steps) != 0 {
		t.Fatalf("group %+v", g)
	}
}

func TestExpandContainerNames(t *testing.T) {
	tmpl := "template: c\ncontainers:\n  tools: { image: alpine }\nsteps:\n  a: { container: tools, run: echo }\n"
	src := `pipeline: app
libraries:
  lib: { repo: r, version: v1 }
containers:
  one-tools: { image: busybox }
steps:
  one: { uses: lib/c }
  two: { uses: lib/c }
  a_very_long_step_name_that_will_not_fit_in_a_container_name: { uses: lib/c }
`
	p := mustExpand(t, src, map[string]string{"c": tmpl})
	seen := map[string]bool{}
	for _, c := range p.Containers {
		if seen[c.Name] || !containerRe.MatchString(c.Name) {
			t.Fatalf("container name %q (seen %v)", c.Name, seen)
		}
		seen[c.Name] = true
	}
	if p.Step("one/a").Container == "one-tools" || p.Step("two/a").Container != "two-tools" {
		t.Fatalf("containers %q %q", p.Step("one/a").Container, p.Step("two/a").Container)
	}
	if c := p.Container(p.Step("one/a").Container); c == nil || c.Image != "alpine" {
		t.Fatalf("one/a runs in %+v", c)
	}
}

func TestExpandKeepsOnlyUsedLibraries(t *testing.T) {
	src := "pipeline: app\nlibraries:\n  lib: { repo: r, version: v1 }\n  unused: { repo: s, version: v1 }\nsteps:\n  s: { uses: lib/c }\n"
	p := mustExpand(t, src, map[string]string{"c": "template: c\nsteps:\n  a: { image: x, run: y }\n"})
	if len(p.Libraries) != 1 || p.Libraries[0].Name != "lib" {
		t.Fatalf("%+v", p.Libraries)
	}
	// A pipeline that uses no template but names a library keeps none.
	p = mustExpand(t, "pipeline: app\nlibraries:\n  lib: { repo: r, version: v1 }\nsteps:\n  s: { image: x, run: y }\n", nil)
	if len(p.Libraries) != 0 {
		t.Fatalf("%+v", p.Libraries)
	}
}

func TestVersions(t *testing.T) {
	for _, v := range []string{"v3", "main", "release/1.x", "v1.2.3+build", testCommit} {
		if !ValidVersion(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"", "-x", "../x", "a..b", "a//b", "a/", "a b", "a;b", strings.Repeat("a", MaxVersionLen+1)} {
		if ValidVersion(v) {
			t.Errorf("%q should not be valid", v)
		}
	}
	if !IsCommitID(testCommit) || IsCommitID("0123456") {
		t.Error("IsCommitID")
	}
}

// The example library in examples/library expands, and the scripts its
// templates run are there and executable.
func TestExampleLibrary(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "library")
	templates := map[string]string{}
	for _, name := range []string{"check", "release"} {
		b, err := os.ReadFile(filepath.Join(dir, TemplatePath(name)))
		if err != nil {
			t.Fatal(err)
		}
		templates[name] = string(b)
	}
	src := `pipeline: demo
libraries:
  platform: { repo: demo-library, version: v1 }
steps:
  release:
    uses: platform/release
    with: { environment: prod }
  quick:
    uses: platform/check
    with: { strict: true }
  after: { image: alpine, needs: release, run: "echo $LAUNCHWAY_NEEDS_RELEASE_URL" }
`
	p := mustExpand(t, src, templates)
	var ids []string
	for _, s := range p.Steps {
		ids = append(ids, s.ID)
		for _, script := range regexp.MustCompile(`\$LAUNCHWAY_LIB/(scripts/[a-z-]+\.sh)`).FindAllStringSubmatch(s.Run, -1) {
			info, err := os.Stat(filepath.Join(dir, script[1]))
			if err != nil || info.Mode()&0o111 == 0 {
				t.Fatalf("%s runs %s: %v %v", s.ID, script[1], err, info)
			}
		}
	}
	want := []string{"release/checks/syntax", "release/checks/notes", "release/build", "release/approve", "release/deploy",
		"quick/syntax", "quick/notes", "after"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("steps %v", ids)
	}
	if d := p.Step("release/deploy"); d.Env["TARGET"] != "prod.example.internal" || d.Env["PARAM_ENVIRONMENT"] != "prod" {
		t.Fatalf("deploy env %v", d.Env)
	}
	if g := p.Group("release"); g.Outputs["url"] != (OutputRef{Step: "release/deploy", Key: "url"}) ||
		g.Outputs["notes"] != (OutputRef{Step: "release/checks/notes", Key: "count"}) {
		t.Fatalf("outputs %v", g.Outputs)
	}
}
