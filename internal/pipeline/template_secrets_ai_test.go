package pipeline

import (
	"strings"
	"testing"
)

func expandWith(t *testing.T, src, tmpl string) (*Pipeline, error) {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	vals, _ := ResolveInputs(p, nil)
	b, err := Bind(p, vals, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range b.Libraries {
		l.Commit = testCommit
	}
	return Expand(b, func(*Library, string) ([]byte, error) { return []byte(tmpl), nil })
}

const secretTmpl = `
template: deliver
params:
  app: { type: string }
  store: { type: string, default: vault }
  count: { type: number, default: 1 }
steps:
  publish:
    image: alpine
    run: ./publish.sh
    secrets:
      TOKEN: { store: { param: store }, key: { param: app, format: "apps/%s/token" }, field: token }
`

func useSecrets(with string) string {
	return "pipeline: p\nlibraries: { platform: { repo: lib, version: v1 } }\nsteps:\n  d:\n    uses: platform/deliver\n    with: { " + with + " }\n"
}

func TestTemplateSecretReferenceFromParameters(t *testing.T) {
	x, err := expandWith(t, useSecrets("app: shop"), secretTmpl)
	if err != nil {
		t.Fatal(err)
	}
	ref := x.Step("d/publish").Secrets["TOKEN"]
	if ref.Store != "vault" || ref.Key != "apps/shop/token" || ref.Field != "token" {
		t.Fatalf("%+v", ref)
	}
	if strings.Contains(mustJSON(x), `"param"`) {
		t.Fatal("the expanded graph holds plain values")
	}
	// A bad store name is still refused after substitution; a missing
	// parameter and an unknown one are template errors.
	if _, err := expandWith(t, useSecrets("app: shop, store: Not_A_Store"), secretTmpl); err == nil || !strings.Contains(err.Error(), "name the store") {
		t.Fatalf("%v", err)
	}
	if _, err := expandWith(t, useSecrets("store: vault"), secretTmpl); err == nil {
		t.Fatal("a missing parameter")
	}
	// A number cannot name a secret.
	bad := strings.Replace(secretTmpl, "key: { param: app, format: \"apps/%s/token\" }", "key: { param: count }", 1)
	if _, err := expandWith(t, useSecrets("app: shop"), bad); err == nil || !strings.Contains(err.Error(), "takes a string parameter") {
		t.Fatalf("%v", err)
	}
	bad = strings.Replace(secretTmpl, "key: { param: app, format: \"apps/%s/token\" }", "key: { param: nope }", 1)
	if _, err := expandWith(t, useSecrets("app: shop"), bad); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("%v", err)
	}
	// The list of templates says which parameters name a secret.
	info := DescribeTemplate([]byte(secretTmpl), "deliver")
	got := map[string]bool{}
	for _, p := range info.Params {
		got[p.Name] = p.SecretReference
	}
	if !got["app"] || !got["store"] || got["count"] {
		t.Fatalf("%v (%v)", got, info.Problems)
	}
}

const aiTmpl = `
template: notes
params:
  model: { type: string }
steps:
  changes:
    image: alpine
    run: git log
  summarise:
    kind: ai
    needs: changes
    model: { param: model }
    prompt: Summarise the changes.
    inputs: [ { label: log, output: changes.log } ]
`

func TestAIStepInATemplate(t *testing.T) {
	src := "pipeline: p\nlibraries: { platform: { repo: lib, version: v1 } }\nsteps:\n  n:\n    uses: platform/notes\n    with: { model: sonnet }\n"
	x, err := expandWith(t, src, aiTmpl)
	if err != nil {
		t.Fatal(err)
	}
	s := x.Step("n/summarise")
	if s == nil || s.AI == nil || s.AI.Model != "sonnet" || s.AI.Inputs[0].Output.Step != "n/changes" {
		t.Fatalf("%+v", s)
	}
	if x.Requires < SchemaVerify {
		t.Fatalf("requires %d", x.Requires)
	}
	if _, err := expandWith(t, strings.Replace(src, "sonnet", "Bad Model", 1), aiTmpl); err == nil || !strings.Contains(err.Error(), "not a model name") {
		t.Fatalf("%v", err)
	}
}
