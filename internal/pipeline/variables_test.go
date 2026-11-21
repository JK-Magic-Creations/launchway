package pipeline

import (
	"strings"
	"testing"
)

const registryTemplate = `template: ship
params:
  registry: { type: string, from: registry, description: Where images go }
  replicas: { type: number, from: replicas, default: 1 }
steps:
  push: { image: alpine, run: "echo $PARAM_REGISTRY $PARAM_REPLICAS" }
`

func shipPipeline(with string) string {
	w := ""
	if with != "" {
		w = "\n    with: " + with
	}
	return "pipeline: app\nlibraries:\n  lib: { repo: r, version: main }\nsteps:\n  ship:\n    uses: lib/ship" + w + "\n"
}

func expandVars(t *testing.T, src string, vars Variables) (*Pipeline, []BoundVariable, error) {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, l := range p.Libraries {
		l.Commit = testCommit
	}
	return ExpandWith(p, func(*Library, string) ([]byte, error) { return []byte(registryTemplate), nil }, vars)
}

func bv(name, typ, text string, locked bool) BoundVariable {
	v := BoundVariable{Name: name, Type: typ, Text: text, Display: text, Source: "wing"}
	if locked {
		v.Locked, v.LockedBy, v.Who = true, "the wing (audited registry)", "an administrator of every project"
	}
	return v
}

func envOf(p *Pipeline) string {
	for _, s := range p.Steps {
		return strings.Join([]string{s.Env["PARAM_REGISTRY"], s.Env["PARAM_REPLICAS"]}, " ")
	}
	return ""
}

func TestTemplateParamTakesAVariable(t *testing.T) {
	p, used, err := expandVars(t, shipPipeline(""), Variables{"registry": bv("registry", "string", "harbor.example", false), "replicas": bv("replicas", "number", "3", false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := envOf(p); got != "harbor.example 3" {
		t.Fatalf("env %q", got)
	}
	if len(used) != 2 || used[0].Name != "registry" {
		t.Fatalf("used %+v", used)
	}
}

func TestTemplateParamTheFileBeatsAnUnlockedVariable(t *testing.T) {
	p, _, err := expandVars(t, shipPipeline("{ registry: mine.example }"), Variables{"registry": bv("registry", "string", "harbor.example", false)})
	if err != nil {
		t.Fatal(err)
	}
	if got := envOf(p); !strings.HasPrefix(got, "mine.example") {
		t.Fatalf("env %q", got)
	}
}

func TestTemplateParamLockedVariableRefusesTheFile(t *testing.T) {
	_, _, err := expandVars(t, shipPipeline("{ registry: mine.example }"), Variables{"registry": bv("registry", "string", "harbor.example", true)})
	ps := problemsOf(t, err)
	p := findProblem(t, ps, "registry is locked to harbor.example by the wing (audited registry)")
	for _, want := range []string{"the pipeline file sets mine.example", "Only an administrator of every project can change it."} {
		if !strings.Contains(p.Message, want) {
			t.Errorf("message %q lacks %q", p.Message, want)
		}
	}
	if p.Line == 0 {
		t.Errorf("the problem names no line")
	}
	// The same value is accepted.
	if _, _, err := expandVars(t, shipPipeline("{ registry: harbor.example }"), Variables{"registry": bv("registry", "string", "harbor.example", true)}); err != nil {
		t.Fatalf("an equal value must be accepted: %v", err)
	}
}

func TestTemplateParamNeedingAVariableSaysWhereToSetIt(t *testing.T) {
	_, _, err := expandVars(t, shipPipeline(""), nil)
	findProblem(t, problemsOf(t, err), "set the variable registry for this project, its unit or its organization (Settings, Variables)")
}

func TestTemplateParamVariableOfTheWrongType(t *testing.T) {
	_, _, err := expandVars(t, shipPipeline(""), Variables{"registry": bv("registry", "number", "3", false), "replicas": bv("replicas", "map", "{}", false)})
	ps := problemsOf(t, err)
	findProblem(t, ps, "the variable registry is a number; the parameter registry is a string")
	findProblem(t, ps, "a map, which the parameter replicas cannot take")
}

func TestInputTakesAVariableBeforeItsDefault(t *testing.T) {
	p, err := Parse([]byte("pipeline: app\ninputs:\n  region: { type: string, from: region, default: us }\n  n: { type: number, from: replicas }\nsteps:\n  a: { image: x, run: y }\n"))
	if err != nil {
		t.Fatal(err)
	}
	vars := Variables{"region": bv("region", "string", "eu", false), "replicas": bv("replicas", "number", "2.0", false)}
	got, used, err := ResolveInputsWith(p, nil, vars)
	if err != nil || got["region"] != "eu" || got["n"] != "2" || len(used) != 2 {
		t.Fatalf("%v %v %v", got, used, err)
	}
	// Given at the start, an unlocked variable gives way; no variable, the default.
	got, _, err = ResolveInputsWith(p, map[string]string{"region": "ap"}, vars)
	if err != nil || got["region"] != "ap" {
		t.Fatalf("%v %v", got, err)
	}
	got, _, err = ResolveInputsWith(p, nil, nil)
	if err != nil || got["region"] != "us" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestInputLockedVariableRefusesTheRunsValue(t *testing.T) {
	p, _ := Parse([]byte("pipeline: app\ninputs:\n  region: { type: string, from: region }\nsteps:\n  a: { image: x, run: y }\n"))
	vars := Variables{"region": bv("region", "string", "eu", true)}
	_, _, err := ResolveInputsWith(p, map[string]string{"region": "ap"}, vars)
	if err == nil || !strings.Contains(err.Error(), "region is locked to eu by the wing (audited registry); this run asked for ap") {
		t.Fatalf("%v", err)
	}
	if got, _, err := ResolveInputsWith(p, map[string]string{"region": "eu"}, vars); err != nil || got["region"] != "eu" {
		t.Fatalf("equal value: %v %v", got, err)
	}
	// A secret or a map is not an input.
	_, _, err = ResolveInputsWith(p, nil, Variables{"region": bv("region", "secret", "", false)})
	if err == nil || !strings.Contains(err.Error(), "secret, which the input region cannot take") {
		t.Fatalf("%v", err)
	}
}

func TestFromMustNameAVariable(t *testing.T) {
	_, err := Parse([]byte("pipeline: app\ninputs:\n  r: { type: string, from: Bad-Name }\nsteps:\n  a: { image: x, run: y }\n"))
	if err == nil || !strings.Contains(err.Error(), "is not a variable name") {
		t.Fatalf("%v", err)
	}
}
