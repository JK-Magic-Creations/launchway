package pipeline

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestReportsKey(t *testing.T) {
	p, err := Parse([]byte(`pipeline: app
steps:
  unit:
    image: python:3.12
    run: pytest --junitxml=reports/unit.xml
    reports:
      tests: reports/*.xml
  go:
    image: golang:1.27
    run: go test -json ./... > reports/go.json
    reports:
      tests: [./reports/go.json, "build/**/TEST-*.xml", reports/go.json]
  plain:
    image: alpine:3.22
    run: "true"
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Step("unit").Reports; got == nil || !slices.Equal(got.Tests, []string{"reports/*.xml"}) {
		t.Fatalf("unit reports: %+v", got)
	}
	// ./ is dropped, and a glob written twice is kept once.
	if got := p.Step("go").Reports.Tests; !slices.Equal(got, []string{"reports/go.json", "build/**/TEST-*.xml"}) {
		t.Fatalf("go reports: %v", got)
	}
	if p.Step("plain").Reports != nil {
		t.Fatal("a step without reports has some")
	}
	if !p.UsesSchema10() {
		t.Fatal("a pipeline with reports does not say it needs schema 10")
	}
	if q, _ := Parse([]byte("pipeline: a\nsteps:\n  a: {image: x, run: y}\n")); q.UsesSchema10() {
		t.Fatal("a pipeline without reports says it needs schema 10")
	}
	// The graph keeps them, and reads them back.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(p)), &back); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(back.Step("go").Reports.Tests, p.Step("go").Reports.Tests) {
		t.Fatalf("round trip: %+v", back.Step("go").Reports)
	}
}

func TestReportsProblems(t *testing.T) {
	cases := []struct {
		name, reports, want string
		line                int
	}{
		{"absolute", "reports:\n      tests: /tmp/x.xml", "not relative to the workspace", 6},
		{"home", "reports:\n      tests: ~/x.xml", "not relative to the workspace", 6},
		{"leaves the workspace", "reports:\n      tests: reports/../../x.xml", "leaves the workspace", 6},
		{"dot dot first", "reports:\n      tests: ../x.xml", "leaves the workspace", 6},
		{"empty", "reports:\n      tests: \"\"", "is empty", 6},
		{"blank", "reports:\n      tests: \"  \"", "is empty", 6},
		{"empty list", "reports:\n      tests: []", "list at least one", 6},
		{"no tests", "reports: {}", "say which files", 5},
		{"not a mapping", "reports: reports/*.xml", "reports is a mapping", 5},
		{"unknown kind", "reports:\n      junit: cover.out", `unknown key "junit"`, 6},
		{"directory", "reports:\n      tests: reports/", "ends with /", 6},
		{"double slash", "reports:\n      tests: reports//x.xml", "empty path part", 6},
		{"stars inside a part", "reports:\n      tests: reports/a**b.xml", "whole path part", 6},
		{"ends with stars", "reports:\n      tests: reports/**", "ends with **", 6},
		{"bad pattern", "reports:\n      tests: \"reports/[x.xml\"", "not a valid pattern", 6},
		{"backslash", "reports:\n      tests: 'reports\\x.xml'", "separate directories with /", 6},
		{"nested list", "reports:\n      tests: [[a.xml]]", "expected a glob or a list", 6},
		{"second item", "reports:\n      tests:\n        - ok.xml\n        - /abs.xml", "not relative", 8},
		{"param outside a template", "reports:\n      tests: { param: x }", "{ param: … } is for templates", 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "pipeline: a\nsteps:\n  a:\n    image: x\n    " + c.reports + "\n    run: y\n"
			_, err := Parse([]byte(src))
			if err == nil {
				t.Fatalf("expected a problem containing %q", c.want)
			}
			ps := err.(Problems)
			for _, p := range ps {
				if strings.Contains(p.Message, c.want) {
					if p.Line != c.line {
						t.Fatalf("%q is on line %d, want %d", p.Message, p.Line, c.line)
					}
					return
				}
			}
			t.Fatalf("got %v, want a problem containing %q", ps, c.want)
		})
	}

	// Too many globs.
	many := "[" + strings.Repeat("a.xml, ", MaxReportGlobs) + "b.xml]"
	if _, err := Parse([]byte("pipeline: a\nsteps:\n  a: {image: x, run: y, reports: {tests: " + many + "}}\n")); err == nil ||
		!strings.Contains(err.Error(), "at most 20 globs") {
		t.Fatalf("too many globs: %v", err)
	}
	// An approval runs nothing, so it writes no reports.
	if _, err := Parse([]byte("pipeline: a\nsteps:\n  a: {kind: approval, message: Ship it, reports: {tests: x.xml}}\n")); err == nil ||
		!strings.Contains(err.Error(), "takes no reports") {
		t.Fatalf("approval with reports: %v", err)
	}
	// A step that uses a template takes the template's.
	if _, err := Parse([]byte("pipeline: a\nlibraries:\n  lib: {repo: r, version: v1}\nsteps:\n  a: {uses: lib/t, reports: {tests: x.xml}}\n")); err == nil ||
		!strings.Contains(err.Error(), "takes no reports") {
		t.Fatalf("uses step with reports: %v", err)
	}
}

func TestCheckReportGlob(t *testing.T) {
	for in, want := range map[string]string{
		"reports/*.xml":          "reports/*.xml",
		"./reports/*.xml":        "reports/*.xml",
		".//reports/x.xml":       "reports/x.xml",
		"**/TEST-*.xml":          "**/TEST-*.xml",
		"a/**/b/**/c.json":       "a/**/b/**/c.json",
		"reports/x?.xml":         "reports/x?.xml",
		"reports/[a-z]*.xml":     "reports/[a-z]*.xml",
		"build/test-results.xml": "build/test-results.xml",
	} {
		got, msg := CheckReportGlob(in)
		if want == "" {
			if msg == "" {
				t.Errorf("%q: accepted as %q, want a problem", in, got)
			}
			continue
		}
		if got != want || msg != "" {
			t.Errorf("%q: got %q %q, want %q", in, got, msg, want)
		}
	}
	if _, msg := CheckReportGlob(strings.Repeat("a", MaxReportGlobLen+1)); !strings.Contains(msg, "longer than") {
		t.Errorf("long glob: %q", msg)
	}
	if _, msg := CheckReportGlob("reports/\x00.xml"); !strings.Contains(msg, "not text") {
		t.Errorf("control character: %q", msg)
	}
}

// A template's step may take its report glob from a parameter, and the
// expanded step keeps the plain value.
func TestTemplateReports(t *testing.T) {
	tmpl := `template: pytest
params:
  dir: { type: string, default: reports }
steps:
  test:
    image: python:3.12
    run: pytest --junitxml="$PARAM_DIR/unit.xml"
    reports:
      tests: [{ param: dir, format: "%s/*.xml" }, extra.xml]
`
	src := `pipeline: app
libraries:
  platform: { repo: ci-templates, version: v1 }
steps:
  ci:
    uses: platform/pytest
    with: { dir: out/junit }
`
	p := mustExpand(t, src, map[string]string{"pytest": tmpl})
	if got := p.Step("ci/test").Reports; got == nil || !slices.Equal(got.Tests, []string{"out/junit/*.xml", "extra.xml"}) {
		t.Fatalf("expanded reports: %+v", got)
	}
	if !p.UsesSchema10() {
		t.Fatal("the expanded pipeline does not say it needs schema 10")
	}

	// A parameter that makes the glob leave the workspace is a problem in
	// the template, on the line of the glob.
	_, err := expand(t, strings.Replace(src, "out/junit", "../outside", 1), map[string]string{"pytest": tmpl})
	ps := problemsOf(t, err)
	got := findProblem(t, ps, "leaves the workspace")
	if got.File != "templates/pytest.yaml" || got.Line != 9 {
		t.Fatalf("problem at %s line %d, want templates/pytest.yaml line 9", got.File, got.Line)
	}
}
