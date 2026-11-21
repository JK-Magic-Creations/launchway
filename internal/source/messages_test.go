package source

import (
	"strings"
	"testing"
)

// The twenty mistakes people make most often writing a pipeline, and the
// exact sentences they are told. A change to a sentence is a change to what
// a person reads at the line they got wrong, so it is made here on purpose.
func TestTheMostLikelyMistakesAreToldPlainly(t *testing.T) {
	const x = "image: x, run: y"
	cases := []struct{ name, file, want string }{
		{"a tab", "pipeline: a\nsteps:\n\tb: {" + x + "}\n", "3: this line has a tab: YAML is indented with spaces, never tabs; replace the tab with two spaces"},
		{"no name", "steps:\n  a: {" + x + "}\n", `1: pipeline: a name is required, for example: pipeline: my-service`},
		{"no steps", "pipeline: a\n", `1: steps: at least one step is required`},
		{"steps as a list", "pipeline: a\nsteps:\n  - {" + x + "}\n", `3: steps: steps is a mapping of step names to steps`},
		{"needs a step that is not there", "pipeline: a\nsteps:\n  a: {" + x + ", needs: b}\n", `3: steps.a.needs: needs "b", but there is no step with that name`},
		{"steps that wait for each other", "pipeline: a\nsteps:\n  a: {" + x + ", needs: b}\n  b: {" + x + ", needs: a}\n", `steps.b.needs: steps need each other in a loop: a → b → a`},
		{"no image", "pipeline: a\nsteps:\n  a: {run: y}\n", `3: steps.a: say what to run in, for example: image: alpine:3.22, or container: one of the containers above`},
		{"no script", "pipeline: a\nsteps:\n  a: {image: x}\n", `3: steps.a.run: a script to run is required`},
		{"a mistyped key", "pipeline: a\nsteps:\n  a: {" + x + ", nedes: b}\n", `unknown key "nedes": did you mean "needs"?`},
		{"a name with a space", "pipeline: a\nsteps:\n  Build Step: {" + x + "}\n", `"Build Step" is not a valid step name: use lower-case letters, digits, hyphens and underscores`},
		{"a timeout with no unit", "pipeline: a\nsteps:\n  a: {" + x + ", timeout: 10}\n", `3: steps.a.timeout: write a duration such as 30s, 10m or 1h`},
		{"retry with words", "pipeline: a\nsteps:\n  a: {" + x + ", retry: lots}\n", `retry is a number of extra attempts, such as retry: 2, or a mapping such as retry: { times: 3, wait: 10s }`},
		{"on as a word", "pipeline: a\non: push\nsteps:\n  a: {" + x + "}\n", `2: on: write what starts a run as a mapping, for example on: { push: { branches: [main] } }`},
		{"another tool's runs-on", "pipeline: a\nruns-on: ubuntu\nsteps:\n  a: {" + x + "}\n", `there is no key called "runs-on" here: choose the runners with runner: { pool: gpu, labels: [cuda] } at the top of the file, or on one step`},
		{"another tool's matrix", "pipeline: a\nsteps:\n  a:\n    matrix: { go: [\"1\"] }\n    image: x\n    run: y\n", `there is no key called "matrix" here: to repeat a step over lists, use for_each: its combinations are the matrix. For example for_each: { go: ["1.26", "1.27"] }`},
		{"another tool's env", "pipeline: a\nenv: { A: b }\nsteps:\n  a: {" + x + "}\n", `there is no key called "env" here: settings for every step go under defaults, as defaults: { env: { TZ: UTC } }`},
		{"a library nobody listed", "pipeline: a\nsteps:\n  a: { uses: platform/go }\n", `3: steps.a.uses: no library called "platform" is listed under libraries:`},
		{"a use with no template", "pipeline: a\nsteps:\n  a: { uses: launchway }\n", `write uses: library/template, such as platform/python-service`},
		{"a starter that does not exist", "pipeline: a\nsteps:\n  a: { uses: launchway/go-servce }\n", `3: steps.a.uses: the library launchway has no template go-servce: did you mean go-service?`},
		{"an input that does not exist", "pipeline: a\nsteps:\n  a: { uses: launchway/go-service, with: { go_vesion: \"1.27\" } }\n", `3: steps.a.with.go_vesion: the template go-service has no parameter "go_vesion": did you mean go_version?`},
		{"an input of the wrong type", "pipeline: a\nsteps:\n  a: { uses: launchway/go-service, with: { race: maybe } }\n", `race is true or false; "maybe" is neither`},
		{"a list with nothing in it", "pipeline: a\nsteps:\n  a:\n    for_each: { go: [] }\n    image: x\n    run: y\n", `4: steps.a.for_each.go: the list is empty`},
		{"a cache with no key", "pipeline: a\nsteps:\n  a: {" + x + ", cache: [{ name: c, paths: [p] }]}\n", `steps.a.cache[0].key: say what the key is made of, such as key: { files: [requirements.txt] }`},
		{"defaults that are not a mapping", "pipeline: a\ndefaults: [a]\nsteps:\n  a: {" + x + "}\n", `2: defaults: defaults is a mapping, for example: defaults: { image: alpine:3.22, env: { TZ: UTC } }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ExpandBuiltin(tc.file)
			if err == nil {
				t.Fatalf("no problem reported; want %q", tc.want)
			}
			// A library that is not the built-in one is a server's to read.
			if !strings.Contains(err.Error(), tc.want) && !strings.Contains(err.Error(), "comes from a server") {
				t.Fatalf("got:\n%s\nwant it to say %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "comes from a server") && tc.name != "a library nobody listed" {
				t.Fatalf("got %v", err)
			}
		})
	}
}
