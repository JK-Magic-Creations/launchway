package pipeline

import (
	"strings"
	"testing"
)

// objectsBuildTemplate is one image: a tag, and the image itself.
const objectsBuildTemplate = `template: build
params:
  repository: { type: string }
  dockerfile: { type: string, default: Dockerfile }
steps:
  tag:
    image: alpine:3.22
    run: echo tag=1 >> "$LAUNCHWAY_OUTPUTS"
  image:
    kind: image
    needs: tag
    file: { param: dockerfile }
    tags: [ { registry: harbor, repository: { param: repository }, tag: { output: tag.tag } } ]
outputs:
  tag: { from: tag.tag }
`

// objectsTemplate delivers one or several applications: a build for each record of apps, and one delivery that
// changes each record's manifest and watches each record's workload.
const objectsTemplate = `template: ship
params:
  apps: { type: objects, fields: [repository, dockerfile, manifest, workload] }
steps:
  build:
    uses: build
    for_each: { app: { param: apps } }
    with:
      repository: { each: app, field: repository }
      dockerfile: { each: app, field: dockerfile, default: Dockerfile }
  deploy:
    kind: deploy
    needs: [build]
    environment: prod
    version: { run: short_sha }
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - for_each: { app: { param: apps } }
            file: { each: app, field: manifest }
            image: { each: app, field: repository }
            tag: { output: { each: app, format: "build[app=%s].tag" } }
      sync:
        resources:
          - for_each: { app: { param: apps } }
            kind: Rollout
            name: { each: app, field: workload }
            namespace: shop
`

const objectsPipeline = `pipeline: shop
libraries:
  platform: { repo: shared, version: v1 }
steps:
  release:
    uses: platform/ship
    with:
      apps:
        - { name: backend, repository: shop/backend, dockerfile: backend/Dockerfile, manifest: apps/shop/base.yaml, workload: shop-backend }
        - { name: frontend, repository: shop/frontend, manifest: apps/shop/base.yaml, workload: shop-frontend }
`

// A template takes a list of records, repeats a step of its own for each, and repeats the entries of its deploy
// step the same way, so that one template delivers one workload or several.
func TestObjectsParameterDrivesRepeatedStepsAndEntries(t *testing.T) {
	p, err := expand(t, objectsPipeline, map[string]string{"ship": objectsTemplate, "build": objectsBuildTemplate})
	if err != nil {
		t.Fatalf("%v", err)
	}
	var builds []string
	for _, s := range p.Steps {
		if s.IsImage() {
			builds = append(builds, s.ID+":"+s.ImageBuild.File)
		}
	}
	want := []string{"release/build[app=backend]/image:backend/Dockerfile", "release/build[app=frontend]/image:Dockerfile"}
	if strings.Join(builds, " ") != strings.Join(want, " ") {
		t.Errorf("one build for each record, each with its own Dockerfile or the default:\n got %v\nwant %v", builds, want)
	}
	d := p.Step("release/deploy")
	if d == nil {
		t.Fatalf("no deploy in %v", ids(p))
	}
	ch := d.ArgoCD.WriteBack.Changes
	if len(ch) != 2 || ch[0].Image != "shop/backend" || ch[1].Image != "shop/frontend" || ch[0].File != "apps/shop/base.yaml" {
		t.Fatalf("one change for each record: %+v", ch)
	}
	if ch[1].Tag.Output.Step != "release/build[app=frontend]" || ch[1].Tag.Output.Key != "tag" {
		t.Errorf("the change reads the tag of its own record's build: %+v", ch[1].Tag.Output)
	}
	rs := d.ArgoCD.Sync.Resources
	if len(rs) != 2 || rs[0].Name != "shop-backend" || rs[1].Name != "shop-frontend" {
		t.Errorf("one resource for each record: %+v", rs)
	}
	// One record is one workload.
	one := strings.Replace(objectsPipeline, "        - { name: frontend, repository: shop/frontend, manifest: apps/shop/base.yaml, workload: shop-frontend }\n", "", 1)
	p, err = expand(t, one, map[string]string{"ship": objectsTemplate, "build": objectsBuildTemplate})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rs := p.Step("release/deploy").ArgoCD.Sync.Resources; len(rs) != 1 || rs[0].Name != "shop-backend" {
		t.Errorf("%+v", rs)
	}
}

func TestObjectsParameterIsChecked(t *testing.T) {
	use := func(apps string) error {
		_, err := expand(t, "pipeline: shop\nlibraries:\n  platform: { repo: shared, version: v1 }\nsteps:\n  release:\n    uses: platform/ship\n    with:\n      apps:\n"+apps,
			map[string]string{"ship": objectsTemplate, "build": objectsBuildTemplate})
		return err
	}
	for name, c := range map[string]struct{ apps, want string }{
		"no name":       {"        - { repository: a/b, manifest: m, workload: w }\n", "has no name"},
		"twice":         {"        - { name: a, repository: a/b, manifest: m, workload: w }\n        - { name: a, repository: a/b }\n", `two records are named "a"`},
		"unknown field": {"        - { name: a, repository: a/b, colour: red }\n", `a field "colour" that the template does not take`},
		"missing field": {"        - { name: a, repository: a/b, dockerfile: D }\n", "has no field manifest"},
		"loop name":     {"        - { name: \"a b\", repository: a/b, manifest: m, workload: w }\n", "cannot be a loop value"},
	} {
		if err := use(c.apps); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An entry of a template may carry if, in every list of a deploy step.
func TestIfOnSyncResources(t *testing.T) {
	const tmpl = `template: d
params:
  both: { type: boolean, default: false }
steps:
  deploy:
    kind: deploy
    environment: prod
    argocd:
      application: shop
      write_back: { repo: gitops, branch: main, changes: [ { file: a.yaml, set: image.tag, value: x } ] }
      sync:
        resources:
          - { kind: Rollout, name: one, namespace: shop }
          - { if: { param: both }, kind: Rollout, name: two, namespace: shop }
`
	for both, n := range map[string]int{"false": 1, "true": 2} {
		p := mustExpand(t, "pipeline: p\nlibraries:\n  platform: { repo: shared, version: v1 }\nsteps:\n  d:\n    uses: platform/d\n    with: { both: "+both+" }\n", map[string]string{"d": tmpl})
		if got := len(p.Step("d/deploy").ArgoCD.Sync.Resources); got != n {
			t.Errorf("both=%s: %d resources, want %d", both, got, n)
		}
	}
}
