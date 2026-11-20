package pipeline

import (
	"strings"
	"testing"
)

func TestOutputConditionParses(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: demo
steps:
  scan: {image: x, run: scan}
  mid: {image: x, run: mid, needs: scan}
  fix: {image: x, run: fix, needs: mid, when: {output: scan.changed, equals: true, branch: main}}
  many: {image: x, run: fix, needs: scan, when: {output: scan.kind, equals: [a, b]}}
`))
	if err != nil {
		t.Fatal(err)
	}
	c := p.Step("fix").When
	if c.Output != "scan.changed" || len(c.Equals) != 1 || c.Equals[0] != "true" || len(c.Branch) != 1 {
		t.Fatalf("%+v", c)
	}
	if got := p.Step("many").When.Equals; len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if p.Requires != SchemaFlow || !p.UsesSchemaFlow() {
		t.Fatalf("requires %d: servers that do not know the key must leave the pipeline alone", p.Requires)
	}
}

func TestOutputConditionMatch(t *testing.T) {
	c := &Condition{Output: "scan.changed", Equals: []string{"true"}}
	outs := map[string]string{"scan.changed": "true"}
	look := func(step, key string) (string, bool) { v, ok := outs[step+"."+key]; return v, ok }
	ctx := Context{Event: EventManual, Output: look}
	if !c.Match(ctx) || c.OutputWhy(ctx) != "" {
		t.Fatal("should match")
	}
	outs["scan.changed"] = "false"
	if c.Match(ctx) || !strings.Contains(c.OutputWhy(ctx), `"false"`) {
		t.Fatal(c.OutputWhy(ctx))
	}
	delete(outs, "scan.changed")
	if c.Match(ctx) || c.OutputWhy(ctx) != "scan.changed was not set" {
		t.Fatal(c.OutputWhy(ctx))
	}
	if c.Match(Context{Event: EventManual}) {
		t.Fatal("without a way to read outputs nothing was set")
	}
	// Like an input test, an output test keeps a step out of a draft.
	outs["scan.changed"] = "true"
	if c.Match(Context{Event: EventDraft, Output: look}) {
		t.Fatal("a draft runs only what names it")
	}
}

func TestOutputConditionProblems(t *testing.T) {
	const head = "pipeline: demo\ninputs:\n  v: {type: string}\nsteps:\n  scan: {image: x, run: scan}\n  other: {image: x, run: o}\n"
	for name, tc := range map[string]struct{ step, want string }{
		"not a needed step":  {"x: {image: x, run: x, needs: other, when: {output: scan.changed, equals: true}}", "not a step this step needs"},
		"nothing needed":     {"x: {image: x, run: x, when: {output: scan.changed, equals: true}}", "not a step this step needs"},
		"unknown step":       {"x: {image: x, run: x, needs: scan, when: {output: nope.changed, equals: true}}", `no step "nope"`},
		"itself":             {"x: {image: x, run: x, needs: scan, when: {output: x.k, equals: true}}", "its own output"},
		"no key":             {"x: {image: x, run: x, needs: scan, when: {output: scan, equals: true}}", "write the step and the output's key"},
		"no equals":          {"x: {image: x, run: x, needs: scan, when: {output: scan.changed}}", "with equals"},
		"input and output":   {"x: {image: x, run: x, needs: scan, when: {input: v, output: scan.changed, equals: a}}", "an input or an output, not both"},
		"a looped step":      {"loop: {image: x, run: l, needs: scan, for_each: {n: [1, 2]}}\n  x: {image: x, run: x, needs: loop, when: {output: loop.k, equals: true}}", "repeats over a list"},
		"empty output value": {"x: {image: x, run: x, needs: scan, when: {output: '', equals: true}}", "name a step"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(head + "  " + tc.step + "\n"))
			ps := problemsOf(t, err)
			p := findProblem(t, ps, tc.want)
			if p.Line == 0 || !strings.HasPrefix(p.Path, "steps.") {
				t.Fatalf("a problem needs a line and a path: %+v", p)
			}
		})
	}
}

func TestOnFailureParses(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: demo
steps:
  a: {image: x, run: a, on_failure: continue}
  b: {image: x, run: b, on_failure: stop}
  c: {kind: image, file: Dockerfile, tags: [{registry: harbor, repository: team/app, tag: {run: short_sha}}], on_failure: continue}
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Step("a").OnFailure != "continue" || p.Step("b").OnFailure != "" || p.Step("c").OnFailure != "continue" {
		t.Fatalf("%q %q %q", p.Step("a").OnFailure, p.Step("b").OnFailure, p.Step("c").OnFailure)
	}
	if p.Requires != SchemaFlow {
		t.Fatal(p.Requires)
	}
	plain, _ := Parse([]byte("pipeline: demo\nsteps:\n  b: {image: x, run: b, on_failure: stop}\n"))
	if plain.Requires != 0 || plain.UsesSchemaFlow() {
		t.Fatal("on_failure: stop is the default and needs no newer server")
	}
}

func TestOnFailureRefusals(t *testing.T) {
	for name, tc := range map[string]struct{ step, want string }{
		"bad value": {"a: {image: x, run: a, on_failure: maybe}", "stop (the default) or continue"},
		"approval":  {"a: {kind: approval, message: 'ok?', on_failure: continue}", "takes no on_failure"},
		"deploy":    {"a: {kind: deploy, environment: prod, on_failure: continue}", "takes no on_failure"},
		"rollback":  {"a: {kind: rollback, deployment: d, on_failure: continue}", "takes no on_failure"},
		"rollout":   {"a: {kind: rollout, action: promote, deployment: d, on_failure: continue}", "takes no on_failure"},
		"verify":    {"a: {kind: verify, on_failure: continue}", "takes no on_failure"},
		"uses":      {"a: {uses: lib/t, on_failure: continue}", "takes no on_failure"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte("pipeline: demo\nlibraries:\n  lib: {repo: platform, version: v1}\nsteps:\n  " + tc.step + "\n"))
			ps := problemsOf(t, err)
			p := findProblem(t, ps, tc.want)
			if p.Line == 0 || !strings.Contains(p.Path, "on_failure") && name != "bad value" {
				t.Fatalf("%+v", p)
			}
		})
	}
}

// A template's step may test an output of another step of the same
// template, and, expanded twice, each copy tests its own: the condition
// names the step the way the run does.
func TestTemplateOutputConditionAndOnFailure(t *testing.T) {
	const tmpl = `template: scanner
steps:
  scan: { image: x, run: scan, on_failure: continue }
  fix:  { image: x, run: fix, needs: scan, when: { output: scan.changed, equals: true } }
`
	p := mustExpand(t, `pipeline: demo
libraries:
  platform: { repo: ci-templates, version: v3 }
steps:
  one: { uses: platform/scanner }
  two: { uses: platform/scanner }
`, map[string]string{"scanner": tmpl})
	if got := p.Step("one/fix").When.Output; got != "one/scan.changed" {
		t.Fatalf("%q", got)
	}
	if got := p.Step("two/fix").When.Output; got != "two/scan.changed" {
		t.Fatalf("the copies of a condition must not share: %q", got)
	}
	if p.Step("one/scan").OnFailure != "continue" || p.Requires != SchemaFlow {
		t.Fatalf("a template's on_failure must make the pipeline need schema %d: requires %d", SchemaFlow, p.Requires)
	}

	_, err := expand(t, `pipeline: demo
libraries:
  platform: { repo: ci-templates, version: v3 }
steps:
  one: { uses: platform/scanner }
`, map[string]string{"scanner": strings.Replace(tmpl, "needs: scan, ", "", 1)})
	findProblem(t, problemsOf(t, err), "not a step this step needs")
}
