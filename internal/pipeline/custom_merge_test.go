package pipeline

import (
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/starters"
)

// expandSteps expands a pipeline whose libraries are maps of files (templates/<name>.yaml, steps/<name>/step.yaml),
// read the way every loader reads them: through TemplateOrStep.
func expandSteps(t *testing.T, src string, requires map[string]string, libs map[string]map[string]string) (*Pipeline, error) {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, l := range p.Libraries {
		l.Commit = testCommit
		if dep := requires[l.Name]; dep != "" {
			l.Requires = []*Library{{Name: dep, Repo: dep + "-repo", Version: "v1", Commit: strings.Repeat("2", 40), RequiredBy: l.Name}}
		}
	}
	return Expand(p, func(l *Library, name string) ([]byte, error) {
		if l.Repo == starters.Name {
			return nil, ErrNoTemplate
		}
		files := libs[strings.TrimSuffix(l.Repo, "-repo")]
		if files == nil {
			files = libs[l.Name]
		}
		return TemplateOrStep(func(path string) ([]byte, error) {
			if s, ok := files[path]; ok {
				return []byte(s), nil
			}
			return nil, ErrNoTemplate
		}, name, false)
	})
}

const mergeStepYAML = `step: notes
contract: 1
toolchain: python@3.13
command: python3 main.py
inputs:
  title: { type: string }
resources: { cpu: 500m, memory: 1Gi }
outputs:
  url: { type: string }
`

// A library's template uses a custom step of a library it requires, by the requirement's name: the steps of
// required libraries resolve as their templates do, and the step knows the library its files are in.
func TestCustomStepOfARequiredLibraryResolves(t *testing.T) {
	src := "pipeline: p\nlibraries:\n  platform: { repo: platform-repo, version: v1 }\nsteps:\n  a: { uses: platform/outer }\n"
	libs := map[string]map[string]string{
		"platform": {"templates/outer.yaml": "template: outer\nsteps:\n  n: { uses: tools/notes, with: { title: Hello } }\n"},
		"tools":    {"steps/notes/step.yaml": mergeStepYAML},
	}
	p, err := expandSteps(t, src, map[string]string{"platform": "tools"}, libs)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Step("a/n/run")
	if s == nil || s.Custom == nil || s.Custom.Name != "notes" || s.Custom.Library != "tools" || s.Library != "tools" {
		t.Fatalf("the step of the required library: %+v", s)
	}
}

// A custom step takes no services: the manifest has none. A stage setting for it is refused with a plain message,
// and so is a step's own services (which a step that uses a template never took).
func TestCustomStepRefusesServices(t *testing.T) {
	libs := map[string]map[string]string{"platform": {"steps/notes/step.yaml": mergeStepYAML}}
	head := "pipeline: p\nlibraries:\n  platform: { repo: platform-repo, version: v1 }\nsteps:\n  n:\n    uses: platform/notes\n"
	_, err := expandSteps(t, head+"    stages: { run: { services: [db] } }\n    with: { title: Hi }\n", nil, libs)
	if err == nil || !strings.Contains(err.Error(), "a custom step takes no services") {
		t.Fatalf("stage services: %v", err)
	}
	if _, err := Parse([]byte(head + "    services: [db]\n    with: { title: Hi }\n")); err == nil || !strings.Contains(err.Error(), "takes no services") {
		t.Fatalf("services on the use: %v", err)
	}
}

// A custom step repeated over the records of an objects parameter is one step for each record, each with its own
// values and the manifest's permissions.
func TestCustomStepRepeatsOverRecords(t *testing.T) {
	tmpl := `template: fan
params:
  targets: { type: objects, fields: [label] }
steps:
  n:
    uses: notes
    for_each: { target: { param: targets } }
    with: { title: { each: target, field: label } }
`
	src := `pipeline: p
libraries:
  platform: { repo: platform-repo, version: v1 }
steps:
  all:
    uses: platform/fan
    with:
      targets:
        - { name: alpha, label: First }
        - { name: beta, label: Second }
`
	libs := map[string]map[string]string{"platform": {"templates/fan.yaml": tmpl, "steps/notes/step.yaml": mergeStepYAML}}
	p, err := expandSteps(t, src, nil, libs)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, s := range p.Steps {
		if s.Custom != nil {
			titles = append(titles, s.ID+"="+s.Custom.Values["title"])
		}
	}
	want := "all/n[target=alpha]/run=First all/n[target=beta]/run=Second"
	if strings.Join(titles, " ") != want {
		t.Fatalf("got %v, want %s", titles, want)
	}
}

// The resources a stage sets for a custom step replace the manifest's, key by key; what the use's own resources say
// fills only what the manifest leaves out.
func TestCustomStepResourcesMergeWithTheManifests(t *testing.T) {
	libs := map[string]map[string]string{"platform": {"steps/notes/step.yaml": mergeStepYAML}}
	base := "pipeline: p\nlibraries:\n  platform: { repo: platform-repo, version: v1 }\nsteps:\n  n:\n    uses: platform/notes\n    with: { title: Hi }\n"
	p, err := expandSteps(t, base, nil, libs)
	if err != nil {
		t.Fatal(err)
	}
	r := p.Step("n/run").Resources
	if r == nil || r.Memory != "1Gi" || r.CPU != "500m" {
		t.Fatalf("the manifest's resources: %+v", r)
	}
	over := strings.Replace(base, "    with:", "    stages: { run: { resources: { memory: 4Gi } } }\n    with:", 1)
	p, err = expandSteps(t, over, nil, libs)
	if err != nil {
		t.Fatal(err)
	}
	r = p.Step("n/run").Resources
	if r == nil || r.Memory != "4Gi" || r.CPU != "500m" {
		t.Fatalf("the stage's memory wins, the manifest's cpu stays: %+v", r)
	}
	plan := strings.Replace(base, "    with:", "    resources: { cpu: 2, memory: 8Gi, memory_limit: 9Gi }\n    with:", 1)
	p, err = expandSteps(t, plan, nil, libs)
	if err != nil {
		t.Fatal(err)
	}
	r = p.Step("n/run").Resources
	if r == nil || r.Memory != "1Gi" || r.CPU != "500m" || r.MemoryLimit != "9Gi" {
		t.Fatalf("the use's resources fill only what the manifest leaves out: %+v", r)
	}
}
