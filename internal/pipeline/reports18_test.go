package pipeline

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestReportsSchema18(t *testing.T) {
	src := `pipeline: a
steps:
  e2e:
    image: mcr.microsoft.com/playwright:v1
    run: npx playwright test
    reports:
      tests:
        paths: [test-results/results.json, "test-results/*.xml"]
        attachments: ./test-results/
      coverage:
        paths: coverage/lcov.info
        fail_under: { lines: 80, branches: 62.5 }
      html:
        - { name: playwright, path: playwright-report }
        - path: target/karate-reports
          index: karate-summary.html
  unit:
    image: python:3.12
    run: pytest
    reports:
      tests: reports/junit.xml
      coverage: [coverage.xml]
  gated:
    image: python:3.12
    run: pytest
    reports:
      coverage: { paths: coverage.xml, fail_under: "75%" }
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	r := p.Step("e2e").Reports
	if !slices.Equal(r.Tests, []string{"test-results/results.json", "test-results/*.xml"}) || r.TestAttachments != "test-results" {
		t.Fatalf("tests: %+v", r)
	}
	if !slices.Equal(r.Coverage, []string{"coverage/lcov.info"}) || r.CoverageFailUnder == nil ||
		*r.CoverageFailUnder != (CoverageThreshold{Lines: 80, Branches: 62.5}) {
		t.Fatalf("coverage: %+v %+v", r.Coverage, r.CoverageFailUnder)
	}
	want := []HTMLReport{{Name: "playwright", Path: "playwright-report", Index: "index.html", Line: 14},
		{Name: "karate-reports", Path: "target/karate-reports", Index: "karate-summary.html", Line: 15}}
	if !slices.Equal(r.HTML, want) {
		t.Fatalf("html: %+v", r.HTML)
	}
	if u := p.Step("unit").Reports; !slices.Equal(u.Coverage, []string{"coverage.xml"}) || u.CoverageFailUnder != nil || u.TestAttachments != "" {
		t.Fatalf("unit: %+v", u)
	}
	if g := p.Step("gated").Reports; g.CoverageFailUnder == nil || g.CoverageFailUnder.Lines != 75 || len(g.Tests) != 0 {
		t.Fatalf("gated: %+v", g)
	}
	if !p.UsesSchema18() || !p.UsesSchema10() {
		t.Fatal("a pipeline with coverage and HTML reports does not say it needs schema 18")
	}
	// The JSON a server keeps reads back the same.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back Pipeline
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if b := back.Step("e2e").Reports; b.TestAttachments != "test-results" || len(b.HTML) != 2 || b.HTML[1].Index != "karate-summary.html" ||
		b.CoverageFailUnder.Branches != 62.5 {
		t.Fatalf("round trip: %+v", b)
	}

	// The keys of schema 10 alone do not need schema 18.
	old, err := Parse([]byte("pipeline: a\nsteps:\n  a: {image: x, run: y, reports: {tests: {paths: a.xml}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if old.UsesSchema18() || !old.UsesSchema10() {
		t.Fatal("tests: { paths } alone is schema 10")
	}
}

func TestReportsSchema18Problems(t *testing.T) {
	for _, c := range []struct{ name, reports, want string }{
		{"attachments outside", "reports: { tests: { paths: a.xml, attachments: ../x } }", "leaves the workspace"},
		{"attachments absolute", "reports: { tests: { paths: a.xml, attachments: /tmp } }", "not relative to the workspace"},
		{"attachments workspace", "reports: { tests: { paths: a.xml, attachments: . } }", "name a directory below the workspace"},
		{"attachments pattern", "reports: { tests: { paths: a.xml, attachments: 'x/*' } }", "not a plain directory name"},
		{"attachments in git", "reports: { tests: { paths: a.xml, attachments: .git/x } }", "inside .git"},
		{"tests without paths", "reports: { tests: { attachments: x } }", "say which files"},
		{"tests unknown key", "reports: { tests: { paths: a.xml, junit: x } }", `unknown key "junit"`},
		{"coverage without paths", "reports: { coverage: { fail_under: 80 } }", "say which files"},
		{"fail under zero", "reports: { coverage: { paths: c.xml, fail_under: 0 } }", "above 0 and at most 100"},
		{"fail under big", "reports: { coverage: { paths: c.xml, fail_under: 101 } }", "above 0 and at most 100"},
		{"fail under word", "reports: { coverage: { paths: c.xml, fail_under: lots } }", "above 0 and at most 100"},
		{"fail under nan", "reports: { coverage: { paths: c.xml, fail_under: .nan } }", "above 0 and at most 100"},
		{"fail under unknown", "reports: { coverage: { paths: c.xml, fail_under: { statements: 80 } } }", `unknown key "statements"`},
		{"coverage outside", "reports: { coverage: ../c.xml }", "leaves the workspace"},
		{"html workspace", "reports: { html: { name: x, path: . } }", "workspace itself cannot be published"},
		{"html outside", "reports: { html: { name: x, path: ../site } }", "leaves the workspace"},
		{"html no path", "reports: { html: { name: x } }", "say which directory to publish"},
		{"html bad name", "reports: { html: { name: My Report, path: site } }", "is not a report name"},
		{"html index outside", "reports: { html: { path: site, index: ../../etc/passwd } }", "not a page inside"},
		{"html index absolute", "reports: { html: { path: site, index: /index.html } }", "not a page inside"},
		{"html twice", "reports: { html: [{ name: a, path: x }, { name: a, path: y }] }", "listed twice"},
		{"html not a mapping", "reports: { html: site }", "each HTML report is a mapping"},
		{"html empty", "reports: { html: [] }", "list at least one report"},
		{"html too many", "reports: { html: [{path: a}, {path: b}, {path: c}, {path: d}, {path: e}, {path: f}] }", "at most 5 HTML reports"},
		{"artifact name taken", "reports: { html: { name: site, path: x } }\n    artifacts: [{ name: report-site, paths: [y] }]", "taken by the HTML report site"},
		{"attachments name taken", "reports: { tests: { paths: a.xml, attachments: x } }\n    artifacts: [{ name: test-attachments, paths: [y] }]", "taken by the test reports"},
		{"embedded name taken", "reports: { tests: a.xml }\n    artifacts: [{ name: test-embedded, paths: [y] }]", "taken by the test reports"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := "pipeline: a\nsteps:\n  a:\n    image: x\n    run: y\n    " + c.reports + "\n"
			_, err := Parse([]byte(src))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// Names nothing else takes stay free.
	if _, err := Parse([]byte("pipeline: a\nsteps:\n  a:\n    image: x\n    run: y\n    artifacts: [{ name: test-attachments, paths: [y] }, { name: report-x, paths: [z] }]\n")); err != nil {
		t.Fatalf("a step without reports may use the names: %v", err)
	}
}

func TestHTMLNameFromPath(t *testing.T) {
	for in, want := range map[string]string{
		"playwright-report":      "playwright-report",
		"target/karate-reports":  "karate-reports",
		"build/Reports/Tests UI": "tests-ui",
		"site/.allure":           "allure",
		"x/___":                  "report",
	} {
		if got := htmlNameFromPath(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
