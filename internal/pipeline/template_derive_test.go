package pipeline

import (
	"strings"
	"testing"
)

const deriveTemplate = `template: service
params:
  name:     { type: string }
  image:    { type: string, default: { param: name, format: "homelab/%s" } }
  manifest: { type: string, default: { param: name, format: "apps/%s/base.yaml" } }
  verify:   { type: boolean, default: true }
  moving:   { type: boolean, default: true }
  health:   { type: string, default: "http://x" }
steps:
  img:
    kind: image
    tags:
      - { registry: harbor, repository: { param: image }, tag: v1 }
      - { registry: harbor, repository: { param: image }, tag: ci-latest, if: { param: moving } }
  approve:
    kind: approval
    needs: img
    message: ok
  deploy:
    kind: deploy
    needs: approve
    environment: prod
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: { param: manifest }, image: { param: image }, tag: v1 }
          - { file: apps/extra.yaml, image: { param: image }, tag: v1, if: { param: verify } }
  verify:
    kind: verify
    if: { param: verify }
    needs: deploy
    deployment: deploy
    duration: 1m
    interval: 30s
    metrics:
      - { name: healthy, source: checks, check: { url: { param: health } } }
      - { name: extra, source: checks, check: { url: "http://y" }, if: { param: verify } }
  rollback:
    kind: rollback
    needs: verify
    deployment: deploy
    if: { param: verify }
`

const deriveUse = `pipeline: shop
libraries:
  platform: { repo: lib, version: v1 }
steps:
  flight:
    uses: platform/service
    with: { name: mcp-weather%s }
`

func TestDerivedDefaultsAndConditionalEntries(t *testing.T) {
	tm := map[string]string{"service": deriveTemplate}
	p := mustExpand(t, strings.Replace(deriveUse, "%s", "", 1), tm)
	img := p.Step("flight/img").ImageBuild
	if len(img.Tags) != 2 || img.Tags[0].Repository != "homelab/mcp-weather" {
		t.Fatalf("tags: %+v", img.Tags)
	}
	ch := p.Step("flight/deploy").ArgoCD.WriteBack.Changes
	if len(ch) != 2 || ch[0].File != "apps/mcp-weather/base.yaml" || ch[0].Image != "homelab/mcp-weather" {
		t.Fatalf("changes: %+v", ch)
	}
	if v := p.Step("flight/verify"); v == nil || len(v.Verify.Metrics) != 2 || p.Step("flight/rollback") == nil {
		t.Fatal("verify and rollback are there by default")
	}

	// A value given beats the derived default; the others derive from the name.
	p = mustExpand(t, strings.Replace(deriveUse, "%s", ", manifest: apps/mcp-servers/base.yaml", 1), tm)
	if f := p.Step("flight/deploy").ArgoCD.WriteBack.Changes[0].File; f != "apps/mcp-servers/base.yaml" {
		t.Fatalf("given value: %s", f)
	}

	// Verify off: the verify and rollback steps are left out, the deploy stays, the entries marked go.
	p = mustExpand(t, strings.Replace(deriveUse, "%s", ", verify: false, moving: false", 1), tm)
	if p.Step("flight/verify") != nil || p.Step("flight/rollback") != nil {
		t.Fatal("verify: false left the steps in")
	}
	if len(p.Step("flight/img").ImageBuild.Tags) != 1 || len(p.Step("flight/deploy").ArgoCD.WriteBack.Changes) != 1 {
		t.Fatal("the entries with if were not left out")
	}
	if d := p.Step("flight/deploy"); d == nil || len(d.Needs) != 1 || d.Needs[0] != "flight/approve" {
		t.Fatalf("deploy: %+v", d)
	}
}

func TestDerivedDefaultMistakesAreSaidPlainly(t *testing.T) {
	use := strings.Replace(deriveUse, "%s", "", 1)
	for want, tmpl := range map[string]string{
		"which this template does not have": strings.Replace(deriveTemplate, "{ param: name, format: \"homelab/%s\" }", "{ param: nope }", 1),
		"from the parameter itself":         strings.Replace(deriveTemplate, "image:    { type: string, default: { param: name, format: \"homelab/%s\" } }", "image:    { type: string, default: { param: image } }", 1),
		"and no other %":                    strings.Replace(deriveTemplate, `format: "homelab/%s"`, `format: "homelab/%s/%d"`, 1),
		"taken from each other":             strings.Replace(strings.Replace(deriveTemplate, "name:     { type: string }", "name:     { type: string, default: { param: image } }", 1), `{ param: name, format: "homelab/%s" }`, `{ param: name }`, 1),
		"is a boolean; a default can be":    strings.Replace(deriveTemplate, `{ param: name, format: "homelab/%s" }`, `{ param: verify }`, 1),
	} {
		_, err := expand(t, use, map[string]string{"service": tmpl})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	// A derived default that does not fit says why, and that a value can be given.
	bad := strings.Replace(deriveTemplate, "verify:   { type: boolean, default: true }", "verify:   { type: boolean, default: true }\n  mode: { type: string, options: [a, b], default: { param: name } }", 1)
	if _, err := expand(t, use, map[string]string{"service": bad}); err == nil || !strings.Contains(err.Error(), "the default of mode, taken from name") {
		t.Errorf("a default that does not fit: %v", err)
	}
	// if outside a template, and on a step kind that has no template, is refused as before.
	_, err := Parse([]byte("pipeline: p\nsteps:\n  a:\n    kind: deploy\n    if: { param: x }\n    environment: prod\n"))
	if err == nil {
		t.Error("if outside a template was accepted on a deploy step")
	}
}
