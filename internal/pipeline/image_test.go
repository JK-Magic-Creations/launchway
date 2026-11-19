package pipeline

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const imageFile = `pipeline: shop
on: { push: { branches: [main] }, pull_request: {} }
inputs:
  channel: { type: choice, options: [stable, edge], default: stable }
steps:
  test:
    image: golang:1.27
    run: echo tag=1.4.2 >> "$LAUNCHWAY_OUTPUTS"
  image:
    kind: image
    description: The shop's image
    needs: test
    context: .
    file: build/Dockerfile
    target: runtime
    platforms: [linux/amd64, linux/arm64]
    build_args: { VERSION: { run: short_sha }, CHANNEL: { input: channel }, RELEASE: { output: test.tag }, PLAIN: "1" }
    secrets:
      - { id: npm_token, secret: { store: vault, key: ci/npm, field: token } }
    tags:
      - { registry: harbor, repository: homelab/shop, tag: { run: short_sha } }
      - { registry: harbor, repository: homelab/shop, tag: latest }
    cache: { registry: harbor, repository: homelab/cache/shop }
    sbom: true
    sign: { key: { store: vault, key: ci/cosign, field: key }, password: { store: vault, key: ci/cosign, field: password } }
    scan: { fail_on: high, mode: enforce }
    timeout: 45m
    retry: 1
  deploy:
    kind: deploy
    needs: image
    environment: prod
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: homelab/shop, digest: { output: image.digest } }
`

func TestImageStepParses(t *testing.T) {
	p, err := Parse([]byte(imageFile))
	if err != nil {
		t.Fatal(err)
	}
	s := p.Step("image")
	b := s.ImageBuild
	if !s.IsImage() || b == nil {
		t.Fatalf("not an image step: %+v", s)
	}
	if b.Context != "." || b.File != "build/Dockerfile" || b.Target != "runtime" || !slices.Equal(b.Platforms, []string{"linux/amd64", "linux/arm64"}) {
		t.Fatalf("build: %+v", b)
	}
	if b.BuildArgs["VERSION"].Run != "short_sha" || b.BuildArgs["CHANNEL"].Input != "channel" || b.BuildArgs["RELEASE"].Output.Step != "test" ||
		b.BuildArgs["PLAIN"].Literal != "1" {
		t.Fatalf("build args: %s", mustJSON(b.BuildArgs))
	}
	if ref := s.Secrets["npm_token"]; ref.Store != "vault" || ref.Key != "ci/npm" || ref.Field != "token" {
		t.Fatalf("secrets: %+v", s.Secrets)
	}
	if len(b.Tags) != 2 || b.Tags[0].Registry != "harbor" || b.Tags[0].Repository != "homelab/shop" || b.Tags[1].Tag.Literal != "latest" {
		t.Fatalf("tags: %s", mustJSON(b.Tags))
	}
	if !b.Pushes() || b.Cache.Repository != "homelab/cache/shop" || !b.SBOM || !b.Provenance || b.Sign.Key.Key != "ci/cosign" ||
		b.Sign.Password.Field != "password" || b.Scan.FailOn != "high" || b.Scan.Mode != "enforce" {
		t.Fatalf("build: %s", mustJSON(b))
	}
	if s.Timeout.D().Minutes() != 45 || s.Retry != 1 {
		t.Fatalf("timeout %s retry %d", s.Timeout.D(), s.Retry)
	}
	// What it keeps, as the engine's reports and artifacts.
	if !slices.Equal(s.Reports.SBOM, []string{imageSBOMGlob}) || !slices.Equal(s.Reports.Findings, []string{imageFindingsGlob}) ||
		len(s.Artifacts) != 1 || s.Artifacts[0].Name != ProvenanceArtifact || s.Artifacts[0].When != ArtifactAlways {
		t.Fatalf("reports %+v artifacts %+v", s.Reports, s.Artifacts)
	}
	if !p.UsesSchema15() || p.Requires != SchemaImages {
		t.Fatalf("schema: requires %d", p.Requires)
	}
	if c := p.Step("deploy").ArgoCD.WriteBack.Changes[0]; c.Digest == nil || c.Digest.Output.Key != "digest" || c.Tag != nil {
		t.Fatalf("deploy change: %+v", c)
	}
	// The graph keeps it all, and reads it back.
	var back Pipeline
	if err := json.Unmarshal([]byte(mustJSON(p)), &back); err != nil {
		t.Fatal(err)
	}
	if mustJSON(back.Step("image")) != mustJSON(s) {
		t.Fatalf("round trip:\n%s\n%s", mustJSON(back.Step("image")), mustJSON(s))
	}
	// Inputs are bound like a deploy step's.
	if p.Bound() {
		t.Fatal("a pipeline whose image step reads an input says it is bound")
	}
	bound, err := Bind(p, map[string]string{"channel": "edge"}, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if v := bound.Step("image").ImageBuild.BuildArgs["CHANNEL"]; v.Literal != "edge" || v.Input != "" {
		t.Fatalf("bound: %+v", v)
	}
	if p.Step("image").ImageBuild.BuildArgs["CHANNEL"].Input != "channel" {
		t.Fatal("Bind changed the pipeline it was given")
	}
}

func TestImageStepDefaults(t *testing.T) {
	p, err := Parse([]byte(`pipeline: a
steps:
  img:
    kind: image
    context: web
    tags: [{ registry: r, repository: team/web, tag: v1 }]
`))
	if err != nil {
		t.Fatal(err)
	}
	b := p.Step("img").ImageBuild
	if b.File != "web/Dockerfile" || !b.Pushes() || !b.Provenance || b.SBOM || b.Sign != nil || b.Scan != nil ||
		p.Step("img").Timeout.D() != DefaultImageTimeout {
		t.Fatalf("defaults: %s", mustJSON(b))
	}
	if p.Step("img").Reports != nil || len(p.Step("img").Artifacts) != 1 {
		t.Fatalf("keeps: %+v %+v", p.Step("img").Reports, p.Step("img").Artifacts)
	}
	p, err = Parse([]byte("pipeline: a\nsteps:\n  img: { kind: image, push: false, provenance: false, sign: true, scan: true }\n"))
	if err != nil {
		t.Fatal(err)
	}
	b = p.Step("img").ImageBuild
	if b.Pushes() || b.Provenance || !b.Sign.Runner || b.Scan.Mode != "report" || len(p.Step("img").Artifacts) != 0 {
		t.Fatalf("%s", mustJSON(b))
	}
}

func TestImageStepProblems(t *testing.T) {
	for _, c := range []struct {
		step string
		want string
		line int
	}{
		{"kind: image\n    run: make", "an image step runs no script", 4},
		{"kind: image\n    push: true", "say what to call the image", 3},
		{"kind: image\n    context: ../x\n    push: false", "relative to the workspace", 4},
		{"kind: image\n    file: /etc/passwd\n    push: false", "relative to the workspace", 4},
		{"kind: image\n    platforms: [arm64]\n    push: false", "not a platform", 4},
		{"kind: image\n    tags: [{ registry: harbor.example.com, repository: a/b, tag: v1 }]", "not a registry name", 4},
		{"kind: image\n    tags: [{ registry: harbor, repository: harbor.example.com/a/b, tag: v1 }]", "names a registry host", 4},
		{"kind: image\n    tags: [{ registry: harbor, repository: Team/App, tag: v1 }]", "lower case", 4},
		{"kind: image\n    tags: [{ registry: harbor, repository: a/b, tag: \"bad tag\" }]", "is not valid", 4},
		{"kind: image\n    tags: [{ registry: harbor, repository: a/b }]", "give the tag", 4},
		{"kind: image\n    tags: [{ registry: harbor, repository: a/b, tag: { output: nowhere.tag } }]", "there is no step nowhere", 3},
		{"kind: image\n    tags: [{ registry: harbor, repository: a/b, tag: { output: other.tag } }]", "not in this step's needs", 3},
		{"kind: image\n    push: false\n    secrets: [{ id: \"bad id\", secret: { store: vault, key: x } }]", "give the secret an id", 5},
		{"kind: image\n    push: false\n    secrets: [{ id: a }]", "say where the secret is kept", 5},
		{"kind: image\n    push: false\n    sign: { keyless: true }", "keyless signing", 5},
		{"kind: image\n    push: false\n    sign: {}", "say which key", 5},
		{"kind: image\n    push: false\n    scan: { mode: enforce }", "needs fail_on", 5},
		{"kind: image\n    push: false\n    scan: { fail_on: severe }", "critical, high, medium or low", 5},
		{"kind: image\n    push: false\n    build_args: { \"1X\": a }", "not a valid build argument name", 5},
		{"kind: image\n    push: false\n    reports: { tests: x }", "keeps its own reports", 5},
		{"kind: image\n    push: false\n    for_each: { v: [a] }", "cannot be repeated", 5},
		{"kind: image\n    push: false\n    timeout: 10s", "between 1m", 5},
		{"kind: image\n    push: false\n    build_args: { A: { input: nope } }", "no input called", 5},
	} {
		src := "pipeline: a\nsteps:\n  other: { image: x, run: y }\n  img:\n    " + c.step + "\n"
		src = strings.Replace(src, "  img:\n    "+c.step, "  img:\n    "+c.step, 1)
		_, err := Parse([]byte(src))
		if err == nil {
			t.Errorf("%q: no problem", c.step)
			continue
		}
		p := findProblem(t, problemsOf(t, err), c.want)
		if p.Line == 0 {
			t.Errorf("%q: no line: %+v", c.step, p)
		}
	}
	// A step reading an image step's output may read only those it sets.
	_, err := Parse([]byte(`pipeline: a
steps:
  img: { kind: image, push: false }
  use: { kind: image, needs: img, tags: [{ registry: r, repository: a/b, tag: { output: img.version } }] }
`))
	findProblem(t, problemsOf(t, err), "an image step's outputs are digest, image, tags, pushed")
}

func TestDeployDigestChange(t *testing.T) {
	base := "pipeline: a\nsteps:\n  b: { image: x, run: y }\n  d:\n    kind: deploy\n    needs: b\n    environment: prod\n    argocd:\n      application: a\n      write_back:\n        repo: gitops\n        branch: main\n        changes:\n          - "
	for change, want := range map[string]string{
		"{ file: a.yaml, image: team/a, tag: v1, digest: { output: b.digest } }": "not both",
		"{ file: a.yaml, image: team/a, digest: sha256:abc }":                    "not a sha256 digest",
		"{ file: a.yaml, image: team/a }":                                        "tag or digest",
	} {
		_, err := Parse([]byte(base + change + "\n"))
		if err == nil {
			t.Errorf("%s: accepted", change)
			continue
		}
		findProblem(t, problemsOf(t, err), want)
	}
	p, err := Parse([]byte(base + "{ file: a.yaml, image: team/a, digest: \"sha256:" + strings.Repeat("ab", 32) + "\" }\n"))
	if err != nil || !p.UsesSchema15() {
		t.Fatalf("%v", err)
	}
}

func TestImageStepInTemplate(t *testing.T) {
	tmpl := `template: image
params:
  repository: { type: string }
steps:
  version:
    image: alpine
    run: echo v=1 >> "$LAUNCHWAY_OUTPUTS"
  build:
    kind: image
    needs: version
    tags: [{ registry: harbor, repository: { param: repository }, tag: { output: version.v } }]
outputs:
  digest: { from: build.digest }
`
	p := mustExpand(t, `pipeline: a
libraries: { lib: { repo: lib, version: v1 } }
steps:
  img: { uses: lib/image, with: { repository: team/shop } }
  deploy:
    kind: deploy
    needs: img
    environment: prod
    argocd:
      application: a
      write_back: { repo: gitops, branch: main, changes: [{ file: a.yaml, image: team/shop, digest: { output: img.digest } }] }
`, map[string]string{"image": tmpl})
	s := p.Step("img/build")
	if s == nil || !s.IsImage() {
		t.Fatalf("steps: %v", mustJSON(p.Steps))
	}
	if tg := s.ImageBuild.Tags[0]; tg.Repository != "team/shop" || tg.Tag.Output.Step != "img/version" {
		t.Fatalf("tag: %s", mustJSON(tg))
	}
	if g := p.Group("img"); g.Outputs["digest"].Step != "img/build" {
		t.Fatalf("group outputs: %+v", g.Outputs)
	}
	_, err := expand(t, "pipeline: a\nlibraries: { lib: { repo: lib, version: v1 } }\nsteps:\n  img: { uses: lib/image, with: { repository: x } }\n",
		map[string]string{"image": strings.Replace(tmpl, "tag: { output: version.v }", "tag: { input: v }", 1)})
	if err == nil || !strings.Contains(err.Error(), "cannot read run inputs") {
		t.Fatalf("input in a template: %v", err)
	}
}
