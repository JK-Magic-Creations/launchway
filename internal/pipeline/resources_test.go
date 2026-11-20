package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

const resourcesHead = "pipeline: p\nsteps:\n"

func TestStepResourcesAreParsed(t *testing.T) {
	p, err := Parse([]byte(resourcesHead + `
  build:
    image: golang:1.27
    run: go build ./...
    resources: { cpu: 4, memory: 6Gi }
  test:
    image: golang:1.27
    run: go test ./...
    resources:
      cpu: 1
      cpu_limit: 8
      memory: 2Gi
      memory_limit: 6Gi
  lint:
    image: golang:1.27
    run: go vet ./...
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := *p.Step("build").Resources; got != (Resources{CPU: "4", Memory: "6Gi"}) {
		t.Errorf("build: %+v", got)
	}
	if got := *p.Step("test").Resources; got != (Resources{CPU: "1", CPULimit: "8", Memory: "2Gi", MemoryLimit: "6Gi"}) {
		t.Errorf("test: %+v", got)
	}
	if p.Step("lint").Resources != nil {
		t.Errorf("lint asked for nothing: %+v", p.Step("lint").Resources)
	}
	if p.Requires != SchemaResources || !p.UsesSchemaResources() {
		t.Errorf("a pipeline that asks for resources needs schema %d, so older servers leave its runs alone: requires %d", SchemaResources, p.Requires)
	}
	a, err := p.Step("build").ResourceAmount()
	if err != nil || a.Memory != 6<<30 || a.CPU != 4000 {
		t.Errorf("%+v %v", a, err)
	}
	// It survives the JSON a run keeps its pipeline in.
	b, _ := json.Marshal(p)
	var back Pipeline
	if err := json.Unmarshal(b, &back); err != nil || back.Step("build").Resources == nil || back.Step("build").Resources.Memory != "6Gi" {
		t.Fatalf("%s: %v", b, err)
	}
	// A pipeline that asks for nothing is as old servers know it.
	q, err := Parse([]byte(resourcesHead + "  a: {image: x, run: a}\n"))
	if err != nil || q.Requires != 0 || q.UsesSchemaResources() {
		t.Errorf("requires %d: %v", q.Requires, err)
	}
	if k := p.Step("build").ResourceKind(); k != KindStep {
		t.Errorf("kind %q", k)
	}
}

func TestStepResourcesAreChecked(t *testing.T) {
	for src, want := range map[string]string{
		"resources: { memory: lots }":                          "steps.a.resources.memory: ",
		"resources: { cpu: 2Gi }":                              "not a CPU amount",
		"resources: { memory: 500m }":                          "not a memory amount",
		"resources: { memory: 8Gi, memory_limit: 4Gi }":        "memory 8Gi is more than memory_limit 4Gi",
		"resources: { cpu: 4, cpu_limit: 2 }":                  "cpu 4 is more than cpu_limit 2",
		"resources: {}":                                        "say what the step needs",
		"resources: { memory: 1Gi, requests: 2 }":              `unknown key "requests"; expected one of: cpu, cpu_limit, memory, memory_limit`,
		"resources: { memory: 0 }":                             "memory",
		"resources: 6Gi":                                       "expected a mapping",
		"resources: { memory: 1Gi, memory: 2Gi }":              `"memory" is written twice`,
		"resources: { limits: { memory: 1Gi } }":               `unknown key "limits"`,
		"resources: { requests: { memory: 1Gi } }":             `unknown key "requests"`,
		"resources: { memory: 1Gi, memory_limit: 1e3 }":        "memory_limit",
		"resources: { memory: -1Gi }":                          "memory",
		"resources: { cpu: 0.0001 }":                           "less than one millicore",
		"resources: { memory: 99999999999999Ti }":              "memory",
		"resources: { memory: 6Gi, cpu_limit: 9999999999999 }": "cpu_limit",
	} {
		_, err := Parse([]byte(resourcesHead + "  a: { image: x, run: a, " + src + " }\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", src, want, err)
		}
	}
}

// Resources are for steps that run a script in a container Launchway sizes.
// The others refuse them, saying why.
func TestOtherKindsRefuseResources(t *testing.T) {
	for name, step := range map[string]string{
		"image":    "kind: image\n    tags: [{ registry: r, repository: a/b, tag: v1 }]\n    resources: { memory: 6Gi }",
		"approval": "kind: approval\n    message: Go on?\n    resources: { memory: 6Gi }",
		"verify":   "kind: verify\n    deployment: a\n    resources: { memory: 6Gi }",
		"ai":       "kind: ai\n    model: local\n    prompt: hi\n    resources: { memory: 6Gi }",
	} {
		_, err := Parse([]byte(resourcesHead + "  a:\n    " + step + "\n"))
		if err == nil || !strings.Contains(err.Error(), "resources") {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if name == "image" && !strings.Contains(err.Error(), "built by the builder") {
			t.Errorf("an image step is built by a builder whose size is not the pipeline's, and the message must say so: %v", err)
		}
		if name == "approval" && !strings.Contains(err.Error(), "an approval runs nothing, so it takes no resources") {
			t.Errorf("%v", err)
		}
	}
	// A step that uses a template takes none either: the template says.
	_, err := Parse([]byte("pipeline: p\nlibraries:\n  platform: { repo: ci, version: v1 }\nsteps:\n  a: { uses: platform/x, resources: { memory: 6Gi } }\n"))
	if err == nil || !strings.Contains(err.Error(), "a step that uses a template takes no resources") {
		t.Errorf("%v", err)
	}
}

func TestResourceKinds(t *testing.T) {
	p, err := Parse([]byte(resourcesHead + "  a: {image: x, run: a}\n  b: {kind: approval, message: ok, needs: a}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Step("a").ResourceKind() != KindStep || p.Step("b").ResourceKind() != "" {
		t.Errorf("%q %q", p.Step("a").ResourceKind(), p.Step("b").ResourceKind())
	}
	for _, k := range ResourceKinds {
		if k != KindStep {
			t.Errorf("%q: only plain steps run in a container Launchway sizes", k)
		}
	}
}

func TestResourcesInATemplateStepReachTheRunsPipeline(t *testing.T) {
	// The template's step asks for resources; the pipeline file does not
	// show it, so expansion marks the pipeline as one that needs schema 22.
	src := "pipeline: p\nlibraries:\n  platform: { repo: ci, version: v1 }\nsteps:\n  b: { uses: platform/build }\n"
	templates := map[string]string{"build": "template: build\nsteps:\n  compile:\n    image: golang:1.27\n    run: go build ./...\n    resources: { cpu: 2, memory: 4Gi }\n"}
	p, err := Parse([]byte(src))
	if err != nil || p.Requires != 0 {
		t.Fatalf("%v requires %d", err, p.Requires)
	}
	out := mustExpand(t, src, templates)
	step := out.Step("b/compile")
	if step == nil || step.Resources == nil || step.Resources.Memory != "4Gi" {
		t.Fatalf("%+v", out.Steps)
	}
	if out.Requires != SchemaResources {
		t.Fatalf("requires %d, want %d", out.Requires, SchemaResources)
	}
}
