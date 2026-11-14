package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const good = `
pipeline: api
on:
  push: { branches: [main, "release/*"] }
steps:
  lint:
    image: alpine:3.22
    run: echo lint
  test:
    image: golang:1.24
    run: go test ./...
    retry: 2
    timeout: 10m
  build:
    image: alpine:3.22
    needs: [lint, test]
    run: echo build
    env: { TARGET: linux }
  deploy:
    image: alpine:3.22
    needs: build
    when: { branch: main, event: [push, manual] }
    run: echo deploy
`

func TestParseGood(t *testing.T) {
	p, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "api" || len(p.Steps) != 4 {
		t.Fatalf("got %+v", p)
	}
	if got := []string{p.Steps[0].ID, p.Steps[1].ID, p.Steps[2].ID, p.Steps[3].ID}; strings.Join(got, ",") != "lint,test,build,deploy" {
		t.Fatalf("file order not kept: %v", got)
	}
	test := p.Step("test")
	if test.Retry != 2 || test.Timeout.D() != 10*time.Minute {
		t.Fatalf("retry/timeout: %+v", test)
	}
	if p.Step("lint").Timeout.D() != DefaultTimeout {
		t.Fatalf("default timeout not applied")
	}
	if got := p.Step("deploy").Needs; len(got) != 1 || got[0] != "build" {
		t.Fatalf("single needs value not accepted as a list: %v", got)
	}
	levels := p.Levels()
	want := [][]string{{"lint", "test"}, {"build"}, {"deploy"}}
	if b1, b2 := mustJSON(levels), mustJSON(want); b1 != b2 {
		t.Fatalf("levels %s, want %s", b1, b2)
	}
	if !p.On.MatchPush("release/1.2", "") || p.On.MatchPush("feature/x", "") {
		t.Fatalf("push matching wrong")
	}
	d := p.Step("deploy").When
	if !d.Match(Context{Event: "push", Branch: "main"}) || d.Match(Context{Event: "push", Branch: "dev"}) {
		t.Fatalf("condition matching wrong")
	}
}

func TestGraphJSONIsStable(t *testing.T) {
	a, _ := Parse([]byte(good))
	b, _ := Parse([]byte(good))
	if mustJSON(a) != mustJSON(b) {
		t.Fatal("same file produced different graphs")
	}
	if strings.Contains(mustJSON(a), "Line") {
		t.Fatal("source positions leaked into the graph")
	}
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(a)), &back); err != nil {
		t.Fatalf("graph does not round-trip: %v", err)
	}
	if mustJSON(&back) != mustJSON(a) {
		t.Fatal("graph changed on round-trip")
	}
}

func TestParseProblems(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"empty", ``, "empty"},
		{"not yaml", "pipeline: [", "did not find"},
		{"no name", "steps:\n  a: {image: x, run: y}\n", "a name is required"},
		{"bad name", "pipeline: My_App\nsteps:\n  a: {image: x, run: y}\n", "not a valid name"},
		{"no steps", "pipeline: a\n", "at least one step"},
		{"unknown top key", "pipeline: a\nstages: {}\nsteps:\n  a: {image: x, run: y}\n", `unknown key "stages"`},
		{"unknown step key", "pipeline: a\nsteps:\n  a: {image: x, run: y, script: z}\n", `unknown key "script"`},
		{"not yet", "pipeline: a\nsteps:\n  a: {image: x, run: y, uses: lib/t}\n", "not supported yet"},
		{"missing image", "pipeline: a\nsteps:\n  a: {run: y}\n", "an image is required"},
		{"missing run", "pipeline: a\nsteps:\n  a: {image: x}\n", "a script to run is required"},
		{"unknown need", "pipeline: a\nsteps:\n  a: {image: x, run: y, needs: [b]}\n", `no step with that name`},
		{"self need", "pipeline: a\nsteps:\n  a: {image: x, run: y, needs: [a]}\n", "cannot need itself"},
		{"cycle", "pipeline: a\nsteps:\n  a: {image: x, run: y, needs: [c]}\n  b: {image: x, run: y, needs: [a]}\n  c: {image: x, run: y, needs: [b]}\n", "loop"},
		{"duplicate step", "pipeline: a\nsteps:\n  a: {image: x, run: y}\n  a: {image: x, run: y}\n", "written twice"},
		{"bad retry", "pipeline: a\nsteps:\n  a: {image: x, run: y, retry: 99}\n", "retry must be"},
		{"bad timeout", "pipeline: a\nsteps:\n  a: {image: x, run: y, timeout: soon}\n", "duration"},
		{"reserved env", "pipeline: a\nsteps:\n  a: {image: x, run: y, env: {SLIPWAY_X: 1}}\n", "reserved"},
		{"bad event", "pipeline: a\nsteps:\n  a: {image: x, run: y, when: {event: pr}}\n", "not known"},
		{"empty push", "pipeline: a\non: {push: {}}\nsteps:\n  a: {image: x, run: y}\n", "list branches or tags"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.src))
			if err == nil {
				t.Fatalf("expected a problem containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %q, want it to contain %q", err.Error(), c.want)
			}
			if _, ok := err.(Problems); !ok {
				t.Fatalf("error is %T, want Problems", err)
			}
		})
	}
}

func TestProblemsHaveLines(t *testing.T) {
	_, err := Parse([]byte("pipeline: a\nsteps:\n  a:\n    image: x\n    run: y\n    colour: blue\n"))
	ps := err.(Problems)
	if ps[0].Line != 6 {
		t.Fatalf("problem reported on line %d, want 6", ps[0].Line)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
