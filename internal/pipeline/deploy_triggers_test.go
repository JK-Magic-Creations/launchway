package pipeline

import (
	"errors"
	"strings"
	"testing"
)

const deployWithInputs = `pipeline: shop
inputs:
  target: { type: choice, options: [staging, prod], default: staging }
  version: { pattern: "^v[0-9]+$", required: true }
steps:
  build:
    image: x
    run: build
  deploy:
    kind: deploy
    needs: build
    environment: { input: target }
    version: { input: version }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { input: version } }
          - { file: apps/shop.yaml, set: metadata.labels.sha, value: { run: sha } }
  smoke:
    needs: deploy
    image: x
    run: smoke
  rollback:
    kind: rollback
    deployment: deploy
    needs: [smoke]
  cleanup:
    needs: [rollback]
    when: { upstream: always }
    image: x
    run: cleanup
`

func TestDeployStepsTakeInputs(t *testing.T) {
	p, err := Parse([]byte(deployWithInputs))
	if err != nil {
		t.Fatal(err)
	}
	if p.Bound() {
		t.Fatal("a deploy step with input values is not bound yet")
	}
	vals, err := ResolveInputs(p, map[string]string{"target": "prod", "version": "v7"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Bind(p, vals, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	d := b.Step("deploy")
	ch := d.ArgoCD.WriteBack.Changes
	if d.Environment != "prod" || d.Version.Literal != "v7" || d.Version.Input != "" || ch[0].Tag.Literal != "v7" || ch[1].Value.Run != "sha" {
		t.Fatalf("bound: env %q version %+v changes %+v %+v", d.Environment, d.Version, ch[0].Tag, ch[1].Value)
	}
	if !b.Bound() || p.Step("deploy").Version.Input != "version" || p.Step("deploy").ArgoCD.WriteBack.Changes[0].Tag.Input != "version" {
		t.Fatal("Bind changed the parsed pipeline, or left inputs in the graph")
	}
	// The graph a run keeps reads back with plain values.
	if s := mustJSON(b); strings.Contains(s, `"input":"version"`) {
		t.Fatalf("an input reference in the run's graph: %s", s)
	}
}

func TestDeployStepRules(t *testing.T) {
	base := strings.Replace(deployWithInputs, "  cleanup:\n    needs: [rollback]\n    when: { upstream: always }\n    image: x\n    run: cleanup\n", "", 1)
	cases := []struct{ name, from, to, want string }{
		{"a looped deploy step", "    kind: deploy\n", "    kind: deploy\n    for_each: { r: [a, b] }\n", "cannot be repeated by for_each"},
		{"a looped rollback step", "    kind: rollback\n", "    kind: rollback\n    max_parallel: 2\n", "cannot be repeated by for_each"},
		{"a deploy step after failures", "    needs: build\n    environment", "    needs: build\n    when: { upstream: always }\n    environment", "takes no upstream"},
		{"a rollback guard that runs after failures", "    needs: deploy\n    image: x\n    run: smoke", "    needs: deploy\n    when: { upstream: always }\n    image: x\n    run: smoke", "would not see them"},
		{"an unknown input", "version: { input: version }", "version: { input: nope }", `no input called "nope"`},
		{"a list input", "  version: { pattern", "  tags: { type: list }\n  version: { pattern", ""},
		{"an environment from a string", "environment: { input: target }", "environment: { input: version }", "may come from a choice input"},
		{"options that are not environments", "options: [staging, prod]", "options: [staging, Prod]", `"Prod" cannot be an environment`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(base, c.from, c.to, 1)
			if src == base {
				t.Fatalf("%q is not in the pipeline", c.from)
			}
			_, err := Parse([]byte(src))
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var ps Problems
			if !errors.As(err, &ps) {
				t.Fatalf("no problems: %v", err)
			}
			for _, p := range ps {
				if strings.Contains(p.Message, c.want) {
					if p.Line == 0 {
						t.Fatalf("no line: %+v", p)
					}
					return
				}
			}
			t.Fatalf("no problem says %q:\n%v", c.want, err)
		})
	}
	// A list input for a deploy value is refused.
	src := strings.Replace(base, "  version: { pattern", "  tags: { type: list }\n  version: { pattern", 1)
	src = strings.Replace(src, "version: { input: version }", "version: { input: tags }", 1)
	if _, err := Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "string, choice or number input") {
		t.Fatalf("a list for a version: %v", err)
	}
	// A cleanup that needs the rollback is fine (the one in deployWithInputs).
	if _, err := Parse([]byte(deployWithInputs)); err != nil {
		t.Fatal(err)
	}
}
