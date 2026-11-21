package pipeline

import (
	"strings"
	"testing"
)

func adaptLib(t *testing.T) (TemplateLoader, string) {
	t.Helper()
	tpl := `template: plan
description: x
params:
  deliver: { type: list, from: launchway_environments, default: [] }
  protected: { type: list, from: launchway_protected, default: [] }
  service: { type: string, from: launchway_project, default: app }
  app: { type: string, default: { text: "{service}-x" } }
steps:
  lint: { image: alpine, run: echo lint }
  test: { image: alpine, needs: lint, run: echo test }
  build: { image: alpine, needs: test, run: echo build }
  ship:
    uses: ship
    if: { param: deliver, empty: false }
    needs: build
    for_each: { environment: { param: deliver } }
    serial: true
    with: { environment: { each: environment } }
`
	ship := `template: ship
description: x
params:
  environment: { type: environment }
  protected: { type: list, from: launchway_protected, default: [] }
steps:
  approve: { kind: approval, message: "go?", if: { param: environment, in: protected } }
  go: { image: alpine, needs: approve, run: echo go }
`
	load := func(l *Library, name string) ([]byte, error) {
		switch name {
		case "plan":
			return []byte(tpl), nil
		case "ship":
			return []byte(ship), nil
		}
		return nil, ErrNoTemplate
	}
	return load, "pipeline: p\nlibraries:\n  lib: { repo: l, version: v1 }\nsteps:\n  ci:\n    uses: lib/plan\n"
}

func expandAdapted(t *testing.T, file string, facts ProjectFacts) (*Pipeline, error) {
	t.Helper()
	load, _ := adaptLib(t)
	p, err := Parse([]byte(file))
	if err != nil {
		return nil, err
	}
	if p, err = Bind(p, PreviewValues(p), BindOptions{}); err != nil {
		return nil, err
	}
	for _, l := range p.Libraries {
		l.Commit = strings.Repeat("a", 40)
	}
	out, _, err := ExpandWith(p, load, WithProjectFacts(nil, facts))
	return out, err
}

func stepIDs(p *Pipeline) string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.ID)
	}
	return strings.Join(out, " ")
}

func TestSerialEnvironmentsFromProjectFacts(t *testing.T) {
	_, file := adaptLib(t)
	p, err := expandAdapted(t, file, ProjectFacts{Project: "shop", Environments: []string{"dev", "prod"}, Protected: []string{"prod"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "ci/lint ci/test ci/build ci/ship[environment=dev]/go ci/ship[environment=prod]/approve ci/ship[environment=prod]/go"
	if got := stepIDs(p); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if n := p.Step("ci/ship[environment=prod]/approve").Needs; !strings.Contains(strings.Join(n, " "), "ci/ship[environment=dev]/go") {
		t.Fatalf("prod does not wait for dev: %v", n)
	}
	// No environments: the delivery is left out, with no error.
	p, err = expandAdapted(t, file, ProjectFacts{Project: "shop"})
	if err != nil || strings.Contains(stepIDs(p), "ship") {
		t.Fatalf("%v %s", err, stepIDs(p))
	}
}

func TestSkipExtendReplace(t *testing.T) {
	_, base := adaptLib(t)
	file := base + "    skip: [lint]\n    extend:\n      - after: test\n        steps:\n          docs: { image: alpine, run: ./docs.sh }\n    replace:\n      build: { image: golang, run: make }\n"
	p, err := expandAdapted(t, file, ProjectFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stepIDs(p), "ci/test ci/docs ci/build"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if n := p.Step("ci/build").Needs; strings.Join(n, " ") != "ci/test ci/docs" && strings.Join(n, " ") != "ci/docs ci/test" {
		t.Fatalf("build must wait for the extension: %v", n)
	}
	if p.Step("ci/build").Image != "golang" {
		t.Fatal("replace did not take over the stage")
	}
	_, err = expandAdapted(t, base+"    skip: [lnt]\n", ProjectFacts{})
	if err == nil || !strings.Contains(err.Error(), `no stage "lnt"; its stages are lint, test, build, ship`) {
		t.Fatalf("%v", err)
	}
}

func TestOneUseFileStandsForAPipeline(t *testing.T) {
	p, err := Parse([]byte("uses: lib/plan\nlibraries:\n  lib: { repo: l, version: v1 }\nwith: { app: x }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "ci" || p.On.Push == nil || len(p.Steps) != 1 || p.Steps[0].Uses != "lib/plan" {
		t.Fatalf("%+v", p)
	}
	if _, err := Parse([]byte("uses: lib/plan\nsteps:\n  a: { image: x, run: y }\n")); err == nil {
		t.Fatal("uses together with steps was accepted")
	}
}

func TestDerivedDefaultTextAndOverride(t *testing.T) {
	_, file := adaptLib(t)
	p, err := expandAdapted(t, file, ProjectFacts{Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if g := p.Group("ci"); g == nil || g.Params["app"] != "shop-x" {
		t.Fatalf("%+v", g)
	}
}
