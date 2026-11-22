package pipeline

import (
	"strings"
	"testing"
)

func TestServicesUsedByManyStepsAreWarnedAbout(t *testing.T) {
	const two = "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true }\n  go: { image: golang:1.27 }\nsteps:\n  migrate: { container: go, run: m }\n  test: { container: go, run: t, needs: migrate }\n"
	p, err := Parse([]byte(two))
	if err != nil {
		t.Fatal(err)
	}
	w := ServiceWarnings(p)
	if len(w) != 1 || w[0].Code != "service-per-step" || w[0].Path != "containers.db" ||
		!strings.Contains(w[0].Message, "migrate, test") || !strings.Contains(w[0].Message, "started again with each step") || !strings.Contains(w[0].Message, "kubernetes.pod: per-run") {
		t.Fatalf("%+v", w)
	}
	for name, text := range map[string]string{
		"one step":    "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true }\nsteps:\n  t: { image: x, run: t }\n",
		"no services": "pipeline: p\nsteps:\n  a: { image: x, run: t }\n  b: { image: x, run: t }\n",
	} {
		p, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if w := ServiceWarnings(p); len(w) != 0 {
			t.Errorf("%s: %+v", name, w)
		}
	}
}

// A step that lists the services it needs starts those and no others; the warning names only the steps that
// would still start the service, and is gone when every step says.
func TestStepsListTheServicesTheyNeed(t *testing.T) {
	const head = "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true }\n  cache: { image: redis:8, service: true }\n  go: { image: golang:1.27 }\nsteps:\n"
	p, err := Parse([]byte(head +
		"  lint: { container: go, run: l, services: [] }\n  migrate: { container: go, run: m, services: [db], needs: lint }\n  test: { container: go, run: t, needs: migrate }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Step("lint"); !s.ServicesSet || len(s.Services) != 0 {
		t.Errorf("services: [] means none: %+v", s)
	}
	if s := p.Step("test"); s.ServicesSet {
		t.Errorf("a step that says nothing keeps every service: %+v", s)
	}
	w := ServiceWarnings(p)
	if len(w) != 1 || w[0].Path != "containers.db" || !strings.Contains(w[0].Message, "migrate, test") {
		t.Fatalf("db is started by migrate and test; cache only by test, which is no warning: %+v", w)
	}
	for _, x := range w {
		if strings.Contains(x.Message, "lint") {
			t.Errorf("a step that starts no service is not named: %s", x.Message)
		}
	}
	p, err = Parse([]byte(head + "  a: { container: go, run: l, services: [db] }\n  b: { container: go, run: m, services: [db, cache], needs: a }\n  c: { container: go, run: t, services: [], needs: b }\n"))
	if err != nil {
		t.Fatal(err)
	}
	w = ServiceWarnings(p)
	if len(w) != 1 || w[0].Path != "containers.db" || !strings.Contains(w[0].Message, "a, b") || strings.Contains(w[0].Message, "c") && strings.Contains(w[0].Message, "(a, b, c)") {
		t.Fatalf("only db is started by two steps: %+v", w)
	}
	for _, c := range []struct{ name, step, want string }{
		{"unknown", "services: [nope]", `no container called "nope"`},
		{"not a service", "services: [go]", `"go" is not a service`},
		{"twice", "services: [db, db]", "listed twice"},
		{"not a list", "services: db", "list the services"},
	} {
		_, err := Parse([]byte(head + "  a: { container: go, run: l, " + c.step + " }\n"))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := Parse([]byte(head + "  a: { container: go, run: l, services: all }\n")); err != nil {
		t.Errorf("all is every service: %v", err)
	}
}

// A flight plan's stage can say what services it starts, from the file that uses it, and a template can name its
// own services and the pipeline's.
func TestFlightPlanStagesTakeServices(t *testing.T) {
	const tmpl = `template: svc
containers:
  own: { image: redis:8, service: true }
  go: { image: golang:1.27 }
steps:
  build: { container: go, run: make, services: [] }
  test: { container: go, run: make test, needs: build, services: [own] }
  lint: { container: go, run: make lint, needs: test }
`
	load := func(*Library, string) ([]byte, error) { return []byte(tmpl), nil }
	expand := func(use string) (*Pipeline, error) {
		p, err := Parse([]byte("pipeline: p\nlibraries:\n  platform: { repo: ci, version: v1 }\ncontainers:\n  db: { image: postgres:17, service: true }\nsteps:\n  ci:\n    uses: platform/svc\n" + use))
		if err != nil {
			return nil, err
		}
		for _, l := range p.Libraries {
			l.Commit = "0123456789012345678901234567890123456789"
		}
		return Expand(p, load)
	}
	p, err := expand("    stages: { lint: { services: [db] }, build: { services: all } }\n")
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Step("ci/build"); s.ServicesSet {
		t.Errorf("services: all takes a stage back to every service: %+v", s)
	}
	if s := p.Step("ci/test"); !s.ServicesSet || len(s.Services) != 1 || !strings.HasSuffix(s.Services[0], "own") || p.Container(s.Services[0]) == nil || !p.Container(s.Services[0]).Service {
		t.Errorf("a template's own service is renamed with its container: %+v", s)
	}
	if s := p.Step("ci/lint"); !s.ServicesSet || len(s.Services) != 1 || s.Services[0] != "db" {
		t.Errorf("a stage names the pipeline's own service: %+v", s)
	}
	if _, err := expand("    stages: { lint: { services: [nope] } }\n"); err == nil || !strings.Contains(err.Error(), `no container called "nope"`) {
		t.Errorf("%v", err)
	}
}
