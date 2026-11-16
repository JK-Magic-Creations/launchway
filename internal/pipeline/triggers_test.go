package pipeline

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const withInputs = `
pipeline: deploy
inputs:
  environment: { type: choice, options: [dev, prod], default: dev, description: Where to deploy }
  version:     { type: string, pattern: "^v[0-9]+\\.[0-9]+\\.[0-9]+$", required: true }
  dry_run:     { type: boolean, default: true }
  replicas:    { type: number, min: 1, max: 20, default: 2 }
  regions:     { type: list, default: [eu, us] }
  note:        { type: string }
steps:
  plan:
    image: alpine:3.22
    run: echo plan
  apply:
    image: alpine:3.22
    needs: plan
    when: { input: dry_run, equals: false }
    run: echo apply
  big:
    image: alpine:3.22
    when: { input: replicas, equals: [10, "20.0"] }
    run: echo big
  prod-only:
    image: alpine:3.22
    when: { input: environment, equals: prod, event: manual }
    run: echo prod
`

func TestInputsParse(t *testing.T) {
	p, err := Parse([]byte(withInputs))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Inputs) != 6 || p.Inputs[0].Name != "environment" || p.Inputs[5].Name != "note" {
		t.Fatalf("inputs not in the order written: %+v", p.Inputs)
	}
	env := p.Input("environment")
	if env.Type != InputChoice || *env.Default != "dev" || env.Description != "Where to deploy" || !reflect.DeepEqual(env.Options, []string{"dev", "prod"}) {
		t.Fatalf("environment: %+v", env)
	}
	if v := p.Input("version"); !v.Required || v.Default != nil || v.Pattern == "" {
		t.Fatalf("version: %+v", v)
	}
	if r := p.Input("replicas"); *r.Min != 1 || *r.Max != 20 || *r.Default != "2" {
		t.Fatalf("replicas: %+v", r)
	}
	if r := p.Input("regions"); *r.Default != `["eu","us"]` {
		t.Fatalf("regions default: %s", *r.Default)
	}
	if c := p.Step("apply").When; c.Input != "dry_run" || !reflect.DeepEqual(c.Equals, []string{"false"}) {
		t.Fatalf("apply's when: %+v", c)
	}
	if c := p.Step("big").When; !reflect.DeepEqual(c.Equals, []string{"10", "20"}) {
		t.Fatalf("numbers are compared in canonical form: %+v", c.Equals)
	}
	if !p.UsesSchema13() {
		t.Fatal("a pipeline with inputs needs schema 13")
	}
	// The graph's JSON is what the run keeps; it reads back the same.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(p)), &back); err != nil || mustJSON(&back) != mustJSON(p) {
		t.Fatalf("JSON round trip: %v\n%s\n%s", err, mustJSON(&back), mustJSON(p))
	}
}

func TestResolveInputs(t *testing.T) {
	p, err := Parse([]byte(withInputs))
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]string{"version": "v1.2.3"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{"version": "v1.2.3"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	got, err := ResolveInputs(p, base)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"environment": "dev", "version": "v1.2.3", "dry_run": "true", "replicas": "2", "regions": `["eu","us"]`, "note": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults: %v", got)
	}
	if err := CheckValues(p, got); err != nil {
		t.Fatalf("resolved values do not pass CheckValues: %v", err)
	}
	cases := []struct {
		name  string
		given map[string]string
		field string // "" means it is accepted
		want  string // a value expected, or a message
	}{
		{"choice", with("environment", "prod"), "", "prod"},
		{"choice not an option", with("environment", "staging"), "environment", `choose one of "dev", "prod"`},
		{"required missing", map[string]string{}, "version", "a value is required"},
		{"required empty", with("version", ""), "version", "a value is required"},
		{"pattern", with("version", "1.2.3"), "version", "does not match the pattern"},
		{"boolean", with("dry_run", "false"), "", "false"},
		{"boolean spelled otherwise", with("dry_run", "no"), "dry_run", "write true or false"},
		{"number canonical", with("replicas", " 3.0 "), "", "3"},
		{"number too small", with("replicas", "0"), "replicas", "at least 1"},
		{"number too big", with("replicas", "21"), "replicas", "at most 20"},
		{"not a number", with("replicas", "two"), "replicas", "not a number"},
		{"not finite", with("replicas", "Inf"), "replicas", "not a number"},
		{"list as JSON", with("regions", `["ap", "eu"]`), "", `["ap","eu"]`},
		{"list with commas", with("regions", "ap, eu"), "", `["ap","eu"]`},
		{"empty list", with("regions", ""), "", `[]`},
		{"bad JSON list", with("regions", `["ap", 3]`), "regions", "JSON array of strings"},
		{"unknown input", with("colour", "red"), "colour", "no input called that"},
		{"long text", with("note", strings.Repeat("x", MaxInputValueLen+1)), "note", "longer than"},
		{"not text", with("note", "a\x00b"), "note", "not text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveInputs(p, c.given)
			if c.field == "" {
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range got {
					if v == c.want {
						return
					}
				}
				t.Fatalf("no value %q in %v", c.want, got)
			}
			var ie InputErrors
			if !errors.As(err, &ie) {
				t.Fatalf("want InputErrors, got %v", err)
			}
			if !strings.Contains(ie[c.field], c.want) {
				t.Fatalf("%s: %q, want %q (all: %v)", c.field, ie[c.field], c.want, ie)
			}
		})
	}
	if err := CheckValues(p, with()); err == nil {
		t.Fatal("CheckValues accepted values that are not complete")
	}
	bad, _ := ResolveInputs(p, base)
	bad["replicas"] = "2.0"
	if err := CheckValues(p, bad); err == nil {
		t.Fatal("CheckValues accepted a value that is not canonical")
	}
}

func TestConditionInputs(t *testing.T) {
	c := &Condition{Input: "env", Equals: []string{"prod", "staging"}}
	for v, want := range map[string]bool{"prod": true, "staging": true, "dev": false, "": false} {
		if got := c.Match(Context{Event: EventManual, Inputs: map[string]string{"env": v}}); got != want {
			t.Errorf("%q: %v", v, got)
		}
	}
	if c.Match(Context{Event: EventManual}) {
		t.Error("a run without the input matched")
	}
	if c.Match(Context{Event: EventDraft, Inputs: map[string]string{"env": "prod"}}) {
		t.Error("an input test ran in a draft that it does not name")
	}
}

// Problems, each on its line.
func TestTriggerProblems(t *testing.T) {
	cases := []struct {
		name, src, want string
		line            int
	}{
		{"unknown type", "inputs:\n  a: { type: date }\n", "the type is string, choice, boolean, number, list", 2},
		{"choice without options", "inputs:\n  a: { type: choice }\n", "a choice lists its options", 2},
		{"default not an option", "inputs:\n  a: { type: choice, options: [x], default: y }\n", "the default does not fit", 2},
		{"options on a string", "inputs:\n  a: { options: [x] }\n", "options is for choice inputs", 2},
		{"bad pattern", "inputs:\n  a: { pattern: \"(\" }\n", "not a regular expression", 2},
		{"pattern on a number", "inputs:\n  a: { type: number, pattern: x }\n", "pattern is for string inputs", 2},
		{"min above max", "inputs:\n  a: { type: number, min: 3, max: 2 }\n", "max is less than min", 2},
		{"default out of range", "inputs:\n  a: { type: number, max: 2, default: 3 }\n", "at most 2", 2},
		{"bad name", "inputs:\n  A: {}\n", "not a valid input name", 2},
		{"same variable", "inputs:\n  a-b: {}\n  a_b: {}\n", "would both be the variable LAUNCHWAY_INPUT_A_B", 3},
		{"options_from", "inputs:\n  a: { type: choice, options: [x], options_from: step }\n", "options_from is not supported yet", 2},
		{"required with push", "on: { push: { branches: [main] } }\ninputs:\n  a: { required: true }\n", "pushes start this pipeline", 3},
		{"required with pull requests", "on: { pull_request: {} }\ninputs:\n  a: { required: true }\n", "pull requests start this pipeline", 3},
		{"when unknown input", "steps:\n  s:\n    image: x\n    run: x\n    when: { input: nope }\n", `no input called "nope"`, 5},
		{"when non-boolean without equals", "inputs:\n  a: {}\nsteps:\n  s:\n    image: x\n    run: x\n    when: { input: a }\n", "say which values run the step", 7},
		{"when equals of the wrong type", "inputs:\n  a: { type: boolean }\nsteps:\n  s:\n    image: x\n    run: x\n    when: { input: a, equals: maybe }\n", "write true or false", 7},
		{"equals without input", "steps:\n  s:\n    image: x\n    run: x\n    when: { equals: x }\n", "equals goes with input", 5},
		{"bad cron", "on:\n  schedule:\n    - cron: \"61 * * * *\"\n", "61 is outside 0-59", 3},
		{"cron with four fields", "on:\n  schedule:\n    - cron: \"* * * *\"\n", "has five fields", 3},
		{"cron that never happens", "on:\n  schedule:\n    - cron: \"0 0 30 2 *\"\n", "never happens", 3},
		{"bad time zone", "on:\n  schedule:\n    - { cron: \"0 3 * * *\", timezone: Mars/Olympus }\n", "is not a time zone", 3},
		{"schedule without cron", "on:\n  schedule:\n    - { timezone: UTC }\n", "say when", 3},
		{"schedule not a list", "on:\n  schedule: { cron: x }\n", "schedule is a list", 2},
		{"same schedule twice", "on:\n  schedule:\n    - cron: \"0 3 * * *\"\n    - cron: \"0 3 * * *\"\n", "the same schedule", 4},
		{"schedule without a required input", "inputs:\n  v: { required: true }\non:\n  schedule:\n    - cron: \"0 3 * * *\"\n", "gives no value for v", 5},
		{"schedule input of the wrong type", "inputs:\n  n: { type: number }\non:\n  schedule:\n    - { cron: \"0 3 * * *\", inputs: { n: lots } }\n", "is not a number", 5},
		{"schedule unknown input", "on:\n  schedule:\n    - { cron: \"0 3 * * *\", inputs: { n: 1 } }\n", `no input called "n"`, 3},
		{"webhook bad name", "on:\n  webhook: { name: Deploy }\n", "not a valid webhook name", 2},
		{"webhook bad path", "inputs:\n  v: {}\non:\n  webhook: { name: d, inputs: { v: version } }\n", "write a field of the request's JSON body", 4},
		{"webhook unknown input", "on:\n  webhook: { name: d, inputs: { v: body.v } }\n", `no input called "v"`, 2},
		{"webhook without a required input", "inputs:\n  v: { required: true }\non:\n  webhook: { name: d }\n", "maps nothing to v", 4},
		{"webhook names twice", "on:\n  webhook:\n    - { name: d }\n    - { name: d }\n", "already a webhook called d", 4},
		{"loop value with a slash", "steps:\n  s:\n    for_each: { os: [a/b] }\n    image: x\n    run: x\n", "cannot be a loop value", 3},
		{"loop value twice", "steps:\n  s:\n    for_each: { os: [a, a] }\n    image: x\n    run: x\n", "listed twice", 3},
		{"empty loop list", "steps:\n  s:\n    for_each: { os: [] }\n    image: x\n    run: x\n", "the list is empty", 3},
		{"exclude without for_each", "steps:\n  s:\n    exclude: []\n    image: x\n    run: x\n", "exclude goes with for_each", 3},
		{"exclude unknown key", "steps:\n  s:\n    for_each: { os: [a] }\n    exclude: [ { arch: x } ]\n    image: x\n    run: x\n", `unknown key "arch"`, 4},
		{"exclude matching nothing", "steps:\n  s:\n    for_each: { os: [a] }\n    exclude: [ { os: b } ]\n    image: x\n    run: x\n", "matches no combination", 4},
		{"each of an unknown list", "steps:\n  s:\n    for_each: { os: [a] }\n    image: { each: arch }\n    run: x\n", `no list called "arch"`, 4},
		{"each without a loop", "steps:\n  s:\n    image: { each: os }\n    run: x\n", "is for steps repeated with for_each", 3},
		{"each in a script", "steps:\n  s:\n    for_each: { os: [a] }\n    image: x\n    run: { each: os }\n", "expected a single value", 5},
		{"each format", "steps:\n  s:\n    for_each: { os: [a] }\n    image: { each: os, format: \"%s-%s\" }\n    run: x\n", "exactly one %s", 4},
		{"loop over a non-list input", "inputs:\n  a: {}\nsteps:\n  s:\n    for_each: { os: { input: a } }\n    image: x\n    run: x\n", "a loop needs an input of type list", 5},
		{"loop over a list default with bad values", "inputs:\n  a: { type: list, default: [\"x y\"] }\nsteps:\n  s:\n    for_each: { os: { input: a } }\n    image: x\n    run: x\n", "cannot be a loop value", 2},
		{"loop from a step", "steps:\n  s:\n    for_each: { os: { from: other.list } }\n    image: x\n    run: x\n", "not supported yet", 3},
		{"max_parallel zero", "steps:\n  s:\n    for_each: { os: [a] }\n    max_parallel: 0\n    image: x\n    run: x\n", "between 1 and", 4},
		{"looped approval", "steps:\n  s:\n    kind: approval\n    message: ok?\n    for_each: { os: [a] }\n", "an approval cannot be repeated", 5},
		{"with input unknown", "libraries: { l: { repo: lib, version: v1 } }\nsteps:\n  s:\n    uses: l/t\n    with: { a: { input: nope } }\n", `no input called "nope"`, 5},
		{"with input format on a number", "inputs:\n  n: { type: number }\nlibraries: { l: { repo: lib, version: v1 } }\nsteps:\n  s:\n    uses: l/t\n    with: { a: { input: n, format: \"x%s\" } }\n", "format is for string and choice inputs", 7},
		{"matrix", "steps:\n  s:\n    image: x\n    run: x\n    matrix: {}\n", "use for_each", 5},
		{"upstream", "steps:\n  s:\n    image: x\n    run: x\n    when: { upstream: sometimes }\n", "upstream is succeeded", 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "pipeline: p\n" + c.src
			if !strings.Contains(c.src, "steps:") {
				src += "steps:\n  zz:\n    image: x\n    run: x\n"
			}
			// Lines in the cases count from the first line of c.src.
			_, err := Parse([]byte(src))
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("no problems: %v", err)
			}
			for _, p := range ps {
				if strings.Contains(p.Message, c.want) {
					if p.Line != c.line+1 {
						t.Fatalf("%q is on line %d, want %d", p.Message, p.Line, c.line+1)
					}
					return
				}
			}
			t.Fatalf("no problem says %q; got:\n%v", c.want, err)
		})
	}
}

func TestTriggersParse(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: nightly
inputs:
  suite: { type: choice, options: [quick, full], default: quick }
  version: { required: true }
on:
  schedule:
    - { cron: "0 3 * * *", timezone: America/Chicago, ref: main, inputs: { suite: full, version: v1 } }
    - { cron: "@hourly", inputs: { version: v2 } }
  webhook:
    name: deploy-request
    inputs: { version: body.release.version, suite: body.suite }
steps:
  s: { image: x, run: x, when: { event: [schedule, webhook] } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.On.Schedule) != 2 || p.On.Schedule[0].Timezone != "America/Chicago" || p.On.Schedule[0].Inputs["suite"] != "full" {
		t.Fatalf("schedules: %+v", p.On.Schedule)
	}
	if p.On.Schedule[0].Key() == p.On.Schedule[1].Key() || p.On.Schedule[0].Key() != p.On.Schedule[0].Key() {
		t.Fatal("schedule keys")
	}
	w := p.On.WebhookNamed("deploy-request")
	if w == nil || w.Inputs["version"] != "body.release.version" {
		t.Fatalf("webhook: %+v", p.On.Webhook)
	}
	vals, err := w.Values([]byte(`{"release":{"version":"v9"},"suite":"full","extra":[1,2]}`))
	if err != nil || vals["version"] != "v9" || vals["suite"] != "full" {
		t.Fatalf("webhook values: %v %v", vals, err)
	}
	if vals, err := w.Values([]byte(`{"release":{}}`)); err != nil || len(vals) != 0 {
		t.Fatalf("missing fields give no value: %v %v", vals, err)
	}
	if _, err := w.Values([]byte(`{"release":{"version":{"x":1}}}`)); err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("an object for a value: %v", err)
	}
	if _, err := w.Values([]byte(`not json`)); err == nil {
		t.Fatal("a body that is not JSON")
	}
	// A list input from a body list, numbers as written.
	lw := &WebhookTrigger{Name: "x", Inputs: map[string]string{"l": "body.l", "n": "body.n", "b": "body.b"}}
	vals, err = lw.Values([]byte(`{"l":["a",2,true],"n":1.50,"b":false}`))
	if err != nil || vals["l"] != `["a","2","true"]` || vals["n"] != "1.50" || vals["b"] != "false" {
		t.Fatalf("typed values: %v %v", vals, err)
	}
}

const loopSrc = `
pipeline: matrix
inputs:
  systems: { type: list, default: [debian, alpine] }
steps:
  lint:
    image: alpine:3.22
    run: echo lint
  test:
    needs: lint
    for_each:
      python: ["3.11", "3.12", "3.13"]
      os: { input: systems }
    exclude: [ { python: "3.11", os: alpine } ]
    max_parallel: 3
    fail_fast: false
    image: { each: python, format: "python:%s" }
    env: { OS: { each: os } }
    run: pytest
  report:
    needs: test
    image: alpine:3.22
    run: echo done
`

func TestLoopExpansion(t *testing.T) {
	p, err := Parse([]byte(loopSrc))
	if err != nil {
		t.Fatal(err)
	}
	if p.Step("test").ForEach == nil || p.Bound() {
		t.Fatal("a parsed loop is not bound yet")
	}
	vals, err := ResolveInputs(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Bind(p, vals, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range b.Steps {
		ids = append(ids, s.ID)
	}
	want := []string{"lint", "test[python=3.11,os=debian]", "test[python=3.12,os=debian]", "test[python=3.12,os=alpine]",
		"test[python=3.13,os=debian]", "test[python=3.13,os=alpine]", "report"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("steps: %v", ids)
	}
	m := b.Step("test[python=3.12,os=alpine]")
	if m.Image != "python:3.12" || m.Env["OS"] != "alpine" || m.Each["python"] != "3.12" || m.Each["os"] != "alpine" || m.Run != "pytest" {
		t.Fatalf("member: %+v", m)
	}
	if !reflect.DeepEqual(m.Needs, []string{"lint"}) {
		t.Fatalf("member needs: %v", m.Needs)
	}
	if got := b.Step("report").Needs; !reflect.DeepEqual(got, want[1:6]) {
		t.Fatalf("needs: test is every member: %v", got)
	}
	if len(b.Loops) != 1 || b.Loops[0].ID != "test" || len(b.Loops[0].Members) != 5 || b.Loops[0].MaxParallel != 3 || b.Loops[0].FailFast {
		t.Fatalf("loop: %+v", b.Loops)
	}
	if p.Step("test").Image != "python:{python}" || p.Step("test[python=3.12,os=alpine]") != nil {
		t.Fatal("Bind changed the parsed pipeline")
	}
	if StepEnvName("test[python=3.12,os=alpine]") != "TEST_PYTHON_3_12_OS_ALPINE" {
		t.Fatal(StepEnvName("test[python=3.12,os=alpine]"))
	}
	// Bound twice, the same IDs: re-runs and resumes rely on it.
	b2, _ := Bind(p, vals, BindOptions{})
	if mustJSON(b2) != mustJSON(b) {
		t.Fatal("binding is not deterministic")
	}
	// The graph keeps what the steps became, and reads back the same.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(b)), &back); err != nil || mustJSON(&back) != mustJSON(b) {
		t.Fatalf("round trip: %v", err)
	}
	// A run input decides the list.
	vals["systems"] = `["arch"]`
	b3, err := Bind(p, vals, BindOptions{})
	if err != nil || len(b3.Loops[0].Members) != 3 {
		t.Fatalf("with one system: %v %+v", err, b3)
	}

	// Limits and empty lists fail at run creation, on the loop's line.
	if _, err := Bind(p, map[string]string{"systems": `["a","b","c"]`}, BindOptions{MaxLoopMembers: 8}); err == nil || !strings.Contains(err.Error(), "allows at most 8") {
		t.Fatalf("limit: %v", err)
	}
	if _, err := Bind(p, map[string]string{"systems": `[]`}, BindOptions{}); err == nil || !strings.Contains(err.Error(), "the input systems, which gives the list os, is empty") {
		t.Fatalf("empty: %v", err)
	}
	if _, err := Bind(p, map[string]string{"systems": `["no spaces"]`}, BindOptions{}); err == nil || !strings.Contains(err.Error(), "cannot be a loop value") {
		t.Fatalf("bad value from an input: %v", err)
	}
	// A preview without values lets the loop stand aside.
	pv, err := Bind(p, map[string]string{"systems": `[]`}, BindOptions{Preview: true})
	if err != nil || pv.Step("report") == nil || !reflect.DeepEqual(pv.Step("report").Needs, []string{"lint"}) {
		t.Fatalf("preview: %v %+v", err, pv)
	}
}

func TestLoopOverTemplate(t *testing.T) {
	src := `
pipeline: lib-loop
libraries: { platform: { repo: lib, version: v1 } }
inputs:
  target: { type: choice, options: [staging, prod], default: staging }
steps:
  build:
    for_each: { arch: [amd64, arm64] }
    uses: platform/build
    with: { arch: { each: arch }, where: { input: target, format: "env-%s" } }
  ship:
    needs: build
    image: alpine:3.22
    run: echo ship
`
	tmpl := map[string]string{"build": `
template: build
params:
  arch: { type: string }
  where: { type: string }
  oses: { type: list, default: [linux, windows] }
steps:
  compile:
    for_each: { os: { param: oses } }
    image: golang:1.24
    run: go build
  pack:
    needs: compile
    image: alpine:3.22
    run: tar
`}
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := ResolveInputs(p, map[string]string{"target": "prod"})
	b, err := Bind(p, vals, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range b.Libraries {
		l.Commit = testCommit
	}
	x, err := Expand(b, func(lib *Library, name string) ([]byte, error) { return []byte(tmpl[name]), nil })
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range x.Steps {
		ids = append(ids, s.ID)
	}
	want := []string{
		"build[arch=amd64]/compile[os=linux]", "build[arch=amd64]/compile[os=windows]", "build[arch=amd64]/pack",
		"build[arch=arm64]/compile[os=linux]", "build[arch=arm64]/compile[os=windows]", "build[arch=arm64]/pack", "ship",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("steps: %v", ids)
	}
	c := x.Step("build[arch=arm64]/compile[os=windows]")
	if c.Env["PARAM_ARCH"] != "arm64" || c.Env["PARAM_WHERE"] != "env-prod" || c.Each["arch"] != "arm64" || c.Each["os"] != "windows" {
		t.Fatalf("member of a member: %+v", c)
	}
	if got := x.Step("build[arch=amd64]/pack").Needs; !reflect.DeepEqual(got, want[0:2]) {
		t.Fatalf("pack needs: %v", got)
	}
	if got := x.Step("ship").Needs; len(got) != 6 {
		t.Fatalf("ship needs every step of every member: %v", got)
	}
	if len(x.Loops) != 3 || x.Loop("build") == nil || x.Loop("build[arch=arm64]/compile") == nil {
		var ls []string
		for _, l := range x.Loops {
			ls = append(ls, l.ID)
		}
		t.Fatalf("loops: %v", ls)
	}
	if refs := x.LoopsOf("build[arch=arm64]/compile[os=linux]"); len(refs) != 2 {
		t.Fatalf("a nested member belongs to two loops: %+v", refs)
	}
}

func TestCron(t *testing.T) {
	chi, _ := time.LoadLocation("America/Chicago")
	at := func(loc *time.Location, s string) time.Time {
		t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			panic(err)
		}
		return t
	}
	cases := []struct {
		expr      string
		loc       *time.Location
		from, nxt string
	}{
		{"0 3 * * *", time.UTC, "2026-11-15 02:59", "2026-11-15 03:00"},
		{"0 3 * * *", time.UTC, "2026-11-15 03:00", "2026-11-16 03:00"},
		{"*/15 * * * *", time.UTC, "2026-11-15 10:07", "2026-11-15 10:15"},
		{"5/20 * * * *", time.UTC, "2026-11-15 10:26", "2026-11-15 10:45"},
		{"0 9-17/4 * * MON-FRI", time.UTC, "2026-11-14 12:00", "2026-11-16 09:00"}, // Saturday → Monday
		{"0 0 1,15 * *", time.UTC, "2026-11-02 00:00", "2026-11-15 00:00"},
		{"0 0 13 * FRI", time.UTC, "2026-11-01 00:00", "2026-11-06 00:00"}, // either day field
		{"0 0 * * 7", time.UTC, "2026-11-15 00:00", "2026-11-22 00:00"},    // 7 is Sunday
		{"@yearly", time.UTC, "2026-06-01 00:00", "2027-01-01 00:00"},
		{"0 0 29 2 *", time.UTC, "2026-03-01 00:00", "2028-02-29 00:00"},
		{"30 2 * * *", chi, "2026-03-07 12:00", "2026-03-09 02:30"}, // 02:30 does not exist on 8 March
		{"30 1 * * *", chi, "2026-11-01 00:00", "2026-11-01 01:30"}, // the first of the two 01:30s
		{"0 3 * * *", chi, "2026-11-14 12:00", "2026-11-15 03:00"},
	}
	for _, c := range cases {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got := cr.Next(at(c.loc, c.from), c.loc)
		if want := at(c.loc, c.nxt); !got.Equal(want) {
			t.Errorf("%s after %s: %s, want %s", c.expr, c.from, got.In(c.loc), want)
		}
	}
	// The second 01:30 of the night the clocks go back does not fire.
	cr, _ := ParseCron("30 1 * * *")
	first := cr.Next(at(chi, "2026-11-01 00:00"), chi)
	if second := cr.Next(first, chi); second.Sub(first) < 24*time.Hour {
		t.Fatalf("fired twice: %s then %s", first, second)
	}
	s := &Schedule{Cron: "0 * * * *"}
	from := time.Date(2026, 11, 15, 0, 30, 0, 0, time.UTC)
	if got := s.Latest(from, from.Add(5*time.Hour+10*time.Minute)); !got.Equal(time.Date(2026, 11, 15, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("latest: %s", got)
	}
	if got := s.Latest(from, from.Add(10*time.Minute)); !got.IsZero() {
		t.Fatalf("latest before the first: %s", got)
	}
	// A minute schedule left alone for a year is still quick to catch up.
	m := &Schedule{Cron: "* * * * *"}
	start := time.Now()
	if got := m.Latest(from, from.AddDate(1, 0, 0)); got.IsZero() {
		t.Fatal("no latest")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("catching up a year took %s", time.Since(start))
	}
}
