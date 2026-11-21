package pipeline

import (
	"strings"
	"testing"
)

func TestDefaultsFillWhatAStepDoesNotSay(t *testing.T) {
	p, err := Parse([]byte(`pipeline: d
defaults:
  image: alpine:3.22
  env: { TZ: UTC, MODE: base }
  timeout: 20m
  retry: 2
steps:
  a: { run: echo a }
  b: { image: golang:1.27, env: { MODE: own, EXTRA: "1" }, timeout: 5m, run: echo b }
  c: { container: tools, run: echo c }
containers:
  tools: { image: busybox }
`))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := p.Step("a"), p.Step("b"), p.Step("c")
	if a.Image != "alpine:3.22" || a.Env["TZ"] != "UTC" || a.Timeout.D() != 20*60*1e9 || a.Retry != 2 {
		t.Fatalf("a: %+v", a)
	}
	if b.Image != "golang:1.27" || b.Env["MODE"] != "own" || b.Env["TZ"] != "UTC" || b.Env["EXTRA"] != "1" || b.Timeout.D() != 5*60*1e9 || b.Retry != 2 {
		t.Fatalf("b: %+v", b)
	}
	if c.Container != "tools" || c.Image != "busybox" {
		t.Fatalf("a step with its own container does not also take the default image: %+v", c)
	}
}

func TestDefaultsMistakesAreSaidPlainly(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"not a mapping", "pipeline: d\ndefaults: [a]\nsteps:\n  a: { image: x, run: y }\n", "defaults is a mapping"},
		{"unknown key", "pipeline: d\ndefaults: { shell: bash }\nsteps:\n  a: { image: x, run: y }\n", "unknown key \"shell\""},
		{"both", "pipeline: d\ndefaults: { image: x, container: y }\nsteps:\n  a: { run: y }\n", "not both"},
		{"top-level env", "pipeline: d\nenv: { A: b }\nsteps:\n  a: { image: x, run: y }\n", "there is no key called \"env\" here: settings for every step go under defaults"},
		{"matrix", "pipeline: d\nsteps:\n  a:\n    matrix: { go: [\"1\"] }\n    image: x\n    run: y\n", "for_each: { go:"},
	} {
		_, err := Parse([]byte(tc.text))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}
