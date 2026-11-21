package pipeline

import (
	"strings"
	"testing"
	"time"
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
	if c.Match(ctx) || c.OutputWhy(ctx) != `the output "changed" of step scan was not set: scan did not write it` {
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

func TestRetryMapForm(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: demo
timeout: 2h
steps:
  a: {image: x, run: a, retry: 2}
  b: {image: x, run: b, retry: {times: 3, wait: 30s, backoff: 2, on: [exit, timeout]}}
  c: {image: x, run: c, retry: {times: 2}}
  d: {kind: image, file: Dockerfile, tags: [{registry: harbor, repository: team/app, tag: {run: short_sha}}], retry: {times: 1, wait: 5s, on: [infrastructure]}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if a := p.Step("a"); a.Retry != 2 || a.RetryFlow() {
		t.Fatalf("retry: 2 must stay what it was: %+v", a)
	}
	b := p.Step("b")
	if b.Retry != 3 || b.RetryWait.D() != 30*time.Second || b.RetryBackoff != 2 || len(b.RetryOn) != 2 || !b.RetryFlow() {
		t.Fatalf("%+v", b)
	}
	if c := p.Step("c"); c.Retry != 2 || c.RetryFlow() {
		t.Fatalf("a map with only times is a whole number: %+v", c)
	}
	if d := p.Step("d"); d.RetryWait.D() != 5*time.Second || d.RetryOn[0] != "infrastructure" {
		t.Fatalf("%+v", d)
	}
	if p.Timeout.D() != 2*time.Hour || p.Requires != SchemaFlow {
		t.Fatalf("timeout %v requires %d", p.Timeout, p.Requires)
	}
	// The pause doubles and stops at an hour.
	for i, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute} {
		if got := b.RetryDelay(i + 1); got != want {
			t.Fatalf("delay before retry %d: %v, want %v", i+1, got, want)
		}
	}
	if got := b.RetryDelay(40); got != time.Hour {
		t.Fatalf("a pause is capped at an hour: %v", got)
	}
	if got := p.Step("a").RetryDelay(1); got != 0 {
		t.Fatalf("no wait, no pause: %v", got)
	}
	if !b.RetriesCause("exit") || b.RetriesCause("infrastructure") || !p.Step("a").RetriesCause("infrastructure") {
		t.Fatal("causes")
	}
	// Only the keys of schema 24 need a newer server.
	old, err := Parse([]byte("pipeline: demo\nsteps:\n  a: {image: x, run: a, retry: 2}\n"))
	if err != nil || old.Requires != 0 || old.UsesSchemaFlow() {
		t.Fatalf("retry: 2 needs nothing newer: %v %+v", err, old)
	}
}

func TestRetryAndTimeoutProblems(t *testing.T) {
	for name, tc := range map[string]struct{ text, want, path string }{
		"unknown cause":  {"a: {image: x, run: a, retry: {times: 1, on: [flaky]}}", `"flaky" is not a cause`, "steps.a.retry.on"},
		"wait over 1h":   {"a: {image: x, run: a, retry: {times: 1, wait: 2h}}", "wait must be between 1s and 1h", "steps.a.retry.wait"},
		"wait too small": {"a: {image: x, run: a, retry: {times: 1, wait: 100ms}}", "wait must be between 1s", "steps.a.retry.wait"},
		"times over 10":  {"a: {image: x, run: a, retry: {times: 11}}", "times must be between 0 and 10", "steps.a.retry.times"},
		"scalar over 10": {"a: {image: x, run: a, retry: 11}", "retry must be between 0 and 10", "steps.a.retry"},
		"no times":       {"a: {image: x, run: a, retry: {wait: 5s}}", "say how many extra attempts", "steps.a.retry"},
		"backoff alone":  {"a: {image: x, run: a, retry: {times: 1, backoff: 2}}", "goes with wait", "steps.a.retry.backoff"},
		"backoff range":  {"a: {image: x, run: a, retry: {times: 1, wait: 1s, backoff: 11}}", "backoff must be between 1 and 10", "steps.a.retry.backoff"},
		"empty on":       {"a: {image: x, run: a, retry: {times: 1, on: []}}", "list at least one cause", "steps.a.retry.on"},
		"twice":          {"a: {image: x, run: a, retry: {times: 1, on: [exit, exit]}}", "exit is listed twice", "steps.a.retry.on"},
		"unknown key":    {"a: {image: x, run: a, retry: {times: 1, delay: 5s}}", `unknown key "delay"`, "steps.a.retry.delay"},
		"approval":       {"a: {kind: approval, message: 'ok?', retry: {times: 1}}", "takes no retry", "steps.a.retry"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte("pipeline: demo\nsteps:\n  " + tc.text + "\n"))
			p := findProblem(t, problemsOf(t, err), tc.want)
			if p.Line == 0 || p.Path != tc.path {
				t.Fatalf("%+v, want path %s with a line", p, tc.path)
			}
		})
	}
	for name, tc := range map[string]struct{ val, want string }{
		"not a duration": {"soon", "write a duration"},
		"too short":      {"10s", "must be between 1m and 720h"},
		"too long":       {"800h", "must be between 1m and 720h"},
	} {
		t.Run("run timeout "+name, func(t *testing.T) {
			_, err := Parse([]byte("pipeline: demo\ntimeout: " + tc.val + "\nsteps:\n  a: {image: x, run: a}\n"))
			p := findProblem(t, problemsOf(t, err), tc.want)
			if p.Line != 2 || p.Path != "timeout" {
				t.Fatalf("%+v", p)
			}
		})
	}
}

// A template may take retry and timeout from its parameters, and a uses
// step's retry map applies to the template's steps that set none.
func TestTemplateRetryMapAndParams(t *testing.T) {
	const tmpl = `template: flaky
params:
  tries: { type: string, default: "2" }
  pause: { type: string, default: 15s }
steps:
  a: { image: x, run: a, retry: { times: { param: tries }, wait: { param: pause }, on: [timeout] } }
  b: { image: x, run: b, needs: a }
`
	p := mustExpand(t, `pipeline: demo
libraries:
  platform: { repo: ci-templates, version: v3 }
steps:
  one: { uses: platform/flaky, with: { tries: "4" } }
  two: { uses: platform/flaky, retry: { times: 1, wait: 1m, backoff: 3 } }
`, map[string]string{"flaky": tmpl})
	a := p.Step("one/a")
	if a.Retry != 4 || a.RetryWait.D() != 15*time.Second || len(a.RetryOn) != 1 {
		t.Fatalf("%+v", a)
	}
	b := p.Step("two/b") // set none of its own: takes the uses step's
	if b.Retry != 1 || b.RetryWait.D() != time.Minute || b.RetryBackoff != 3 {
		t.Fatalf("%+v", b)
	}
	if two := p.Step("two/a"); two.Retry != 2 || two.RetryWait.D() != 15*time.Second {
		t.Fatalf("a step that sets its own keeps it: %+v", two)
	}
	if p.Requires != SchemaFlow {
		t.Fatal(p.Requires)
	}
}

func TestLaneParses(t *testing.T) {
	p, err := Parse([]byte(`
pipeline: demo
inputs:
  target: {type: choice, options: [a, b], default: a}
  who: {type: string, default: x}
lane:
  per: [ref, pull_request, "input:target"]
  when_busy: cancel
steps:
  a: {image: x, run: a}
`))
	if err != nil {
		t.Fatal(err)
	}
	l := p.Lane
	if l.Name != "demo" || l.WhenBusy != "cancel" || l.Queue != "newest" || len(l.Per) != 3 || p.Requires != SchemaFlow {
		t.Fatalf("%+v requires %d", l, p.Requires)
	}
	got := l.LaneKey("refs/heads/main", 12, map[string]string{"target": "b"})
	if got != "demo@refs/heads/main#12[target=b]" {
		t.Fatal(got)
	}
	if got := l.Label("refs/heads/main", 12, map[string]string{"target": "b"}); got != "demo/ref main/pull request 12/target b" {
		t.Fatal(got)
	}
	plain, err := Parse([]byte("pipeline: demo\nlane: {name: release}\nsteps:\n  a: {image: x, run: a}\n"))
	if err != nil || plain.Lane.WhenBusy != "wait" || plain.Lane.Name != "release" || plain.Lane.LaneKey("refs/heads/x", 0, nil) != "release" {
		t.Fatalf("%v %+v", err, plain)
	}
}

func TestLaneProblems(t *testing.T) {
	const steps = "steps:\n  a: {image: x, run: a}\n"
	for name, tc := range map[string]struct{ text, want, path string }{
		"supersede too":     {"supersede: true\nlane: {name: x}\n", "supersede or a lane, not both", "lane"},
		"supersede after":   {"lane: {name: x}\nsupersede: true\n", "supersede or a lane, not both", "lane"},
		"bad name":          {"lane: {name: Release}\n", "is not a lane name", "lane.name"},
		"bad per":           {"lane: {per: [branch]}\n", "not something a lane can be split by", "lane.per"},
		"per twice":         {"lane: {per: [ref, ref]}\n", "ref is listed twice", "lane.per"},
		"bad when_busy":     {"lane: {when_busy: queue}\n", "wait (the default), cancel or skip", "lane.when_busy"},
		"bad queue":         {"lane: {queue: some}\n", "newest (the default) or all", "lane.queue"},
		"queue with cancel": {"lane: {when_busy: cancel, queue: all}\n", "queue is for when_busy: wait", "lane.queue"},
		"unknown key":       {"lane: {group: x}\n", `unknown key "group"`, "lane.group"},
		"unknown input":     {"lane: {per: [\"input:nope\"]}\n", `there is no input called "nope"`, "lane.per"},
		"bad input type":    {"inputs:\n  n: {type: number, default: 1}\nlane: {per: [\"input:n\"]}\n", "a lane can be split by a string or choice input", "lane.per"},
		"not a mapping":     {"lane: wait\n", "expected a mapping", "lane"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte("pipeline: demo\n" + tc.text + steps))
			p := findProblem(t, problemsOf(t, err), tc.want)
			if p.Line == 0 || p.Path != tc.path {
				t.Fatalf("%+v, want path %s with a line", p, tc.path)
			}
		})
	}
}

func TestPullRequestInto(t *testing.T) {
	p, err := Parse([]byte("pipeline: demo\non: { pull_request: { into: [main, \"release/*\"] } }\nsteps:\n  a: {image: x, run: a}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.On.FiltersPullRequests() || p.Requires != SchemaFlow || !p.UsesSchema9() {
		t.Fatalf("%+v requires %d", p.On.PullRequest, p.Requires)
	}
	for branch, want := range map[string]bool{"main": true, "release/1.2": true, "release": false, "staging": false, "": false} {
		if got := p.On.MatchPullRequestInto(branch); got != want {
			t.Errorf("into %q: %v, want %v", branch, got, want)
		}
	}
	// A single pattern, as a list of one.
	one, err := Parse([]byte("pipeline: demo\non: { pull_request: { into: main } }\nsteps:\n  a: {image: x, run: a}\n"))
	if err != nil || len(one.On.PullRequest.Into) != 1 || one.On.PullRequest.Into[0] != "main" {
		t.Fatalf("%v %+v", err, one.On.PullRequest)
	}
	// pull_request: {} keeps its meaning: every pull request, no forge, nothing newer needed.
	for _, text := range []string{"on: { pull_request: {} }", "on: { pull_request: }"} {
		q, err := Parse([]byte("pipeline: demo\n" + text + "\nsteps:\n  a: {image: x, run: a}\n"))
		if err != nil || q.On.FiltersPullRequests() || !q.On.MatchPullRequestInto("anything") || q.Requires != 0 || q.UsesSchemaFlow() {
			t.Fatalf("%s: %v %+v", text, err, q)
		}
		if j := mustJSON(q); !strings.Contains(j, `"pull_request":{}`) {
			t.Fatalf("%s", j)
		}
	}
}

func TestPullRequestIntoProblems(t *testing.T) {
	for name, tc := range map[string]struct{ on, want, path string }{
		"branches": {"{ pull_request: { branches: [main] } }", "pull_request: { into:", "on.pull_request.branches"},
		"tags":     {"{ pull_request: { tags: [v1] } }", "pull_request: { into:", "on.pull_request.tags"},
		"empty":    {"{ pull_request: { into: [] } }", "list the branches pull requests may target", "on.pull_request.into"},
		"pattern":  {"{ pull_request: { into: [\"[\"] } }", "pattern", "on.pull_request.into"},
		"unknown":  {"{ pull_request: { labels: [x] } }", `unknown key "labels"`, "on.pull_request.labels"},
		"scalar":   {"{ pull_request: yes }", "pull_request: { into: [main] }", "on.pull_request"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte("pipeline: demo\non: " + tc.on + "\nsteps:\n  a: {image: x, run: a}\n"))
			p := findProblem(t, problemsOf(t, err), tc.want)
			if p.Line == 0 || p.Path != tc.path {
				t.Fatalf("%+v, want path %s with a line", p, tc.path)
			}
		})
	}
}

// A when: { output } test of a key nothing writes is a warning at the line
// of the test; a key the script mentions, or a fixed output, is not.
func TestOutputWarnings(t *testing.T) {
	const head = "pipeline: demo\nsteps:\n  scan: {image: x, run: 'echo changed=true >> $LAUNCHWAY_OUTPUTS'}\n"
	warn := func(rest string) Problems {
		t.Helper()
		p, err := Parse([]byte(head + rest))
		if err != nil {
			t.Fatal(err)
		}
		return OutputWarnings(p)
	}
	if ws := warn("  fix: {image: x, run: f, needs: scan, when: {output: scan.changed, equals: true}}\n"); len(ws) != 0 {
		t.Fatalf("the key is written: %+v", ws)
	}
	ws := warn("  fix: {image: x, run: f, needs: scan, when: {output: scan.chnaged, equals: true}}\n")
	if len(ws) != 1 || ws[0].Line != 4 || ws[0].Path != "steps.fix.when.output" ||
		!strings.Contains(ws[0].Message, `"chnaged" of step scan`) || !strings.Contains(ws[0].Message, "will be skipped every time") {
		t.Fatalf("%+v", ws)
	}
	// A step of a kind with fixed outputs is checked against them.
	ai := "  ask: {kind: ai, model: m, prompt: hi, needs: scan}\n"
	if ws := warn(ai + "  fix: {image: x, run: f, needs: ask, when: {output: ask.text, equals: x}}\n"); len(ws) != 0 {
		t.Fatalf("ai steps write text: %+v", ws)
	}
	if ws := warn(ai + "  fix: {image: x, run: f, needs: ask, when: {output: ask.nope, equals: x}}\n"); len(ws) != 1 {
		t.Fatalf("%+v", ws)
	}
}
