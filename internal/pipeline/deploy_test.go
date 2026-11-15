package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

const deployFile = `pipeline: beacon
steps:
  build:
    image: alpine:3.22
    run: ./ci/build.sh
  deploy-prod:
    kind: deploy
    needs: build
    when: { branch: main }
    environment: prod
    service: beacon
    version: { output: build.tag }
    timeout: 30m
    argocd:
      application: beacon
      write_back:
        repo: gitops
        branch: main
        changes:
          - file: apps/beacon/base.yaml
            image: homelab/beacon
            tag: { output: build.tag }
          - file: helm-values/launchway-dev.yaml
            set: image.tag
            value: { output: build.tag }
          - file: argocd-apps/launchway-dev.yaml
            set: spec.sources[0].targetRevision
            value: { run: sha }
      sync:
        request: auto
        resources:
          - { kind: Rollout, name: beacon-hub, namespace: beacon }
        prune: false
        windows: wait
      health: resources
    rollout:
      name: beacon-hub
      namespace: beacon
      until: healthy
    on_cancel: leave
  smoke:
    needs: deploy-prod
    image: curlimages/curl:8.10.1
    run: ./ci/smoke.sh
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [smoke]
    timeout: 15m
`

func TestDeployStepParses(t *testing.T) {
	p, err := Parse([]byte(deployFile))
	if err != nil {
		t.Fatal(err)
	}
	d := p.Step("deploy-prod")
	if !d.IsDeploy() || d.Environment != "prod" || d.Service != "beacon" || d.Version.Output.Step != "build" || d.Timeout.Short() != "30m" {
		t.Fatalf("deploy step: %+v", d)
	}
	a := d.ArgoCD
	if a.Application != "beacon" || a.Health != "resources" || a.Sync.Request != "auto" || a.Sync.Windows != "wait" ||
		len(a.Sync.Resources) != 1 || a.Sync.Resources[0].Group != "argoproj.io" || len(a.WriteBack.Changes) != 3 ||
		a.WriteBack.Changes[2].Value.Run != "sha" || a.WriteBack.Changes[1].Set != "image.tag" {
		t.Fatalf("argocd: %+v", a)
	}
	if d.Rollout.Name != "beacon-hub" || d.OnCancel != "leave" {
		t.Fatalf("rollout: %+v", d.Rollout)
	}
	r := p.Step("rollback-prod")
	if !r.IsRollback() || r.Deployment != "deploy-prod" || r.Timeout.Short() != "15m" {
		t.Fatalf("rollback: %+v", r)
	}
	if got := strings.Join(p.DeploymentScope("deploy-prod"), ","); got != "deploy-prod,smoke,rollback-prod" {
		t.Fatalf("scope: %s", got)
	}
	if !p.UsesSchema11() || len(p.RunContainers()) != 2 || p.ContainerFor(d) != "" {
		t.Fatalf("containers: %+v", p.RunContainers())
	}
	// The graph is stored as JSON with each run, and read back strictly.
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var back Pipeline
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if back.Step("deploy-prod").ArgoCD.WriteBack.Changes[0].Tag.Output.Key != "tag" {
		t.Fatal("the graph did not survive JSON")
	}
}

func TestDeployDefaults(t *testing.T) {
	p, err := Parse([]byte(`pipeline: x
steps:
  d:
    kind: deploy
    environment: dev
    argocd:
      application: x
      write_back: { repo: gitops, branch: main, changes: [ { file: a.yaml, set: image.tag, value: "1" } ] }
`))
	if err != nil {
		t.Fatal(err)
	}
	d := p.Step("d")
	if d.Version.Run != "short_sha" || d.Timeout.Short() != "30m" || d.ArgoCD.Health != "application" || d.ArgoCD.Sync.Request != "auto" ||
		d.ArgoCD.Sync.Windows != "wait" || d.OnCancel != "leave" {
		t.Fatalf("defaults: %+v %+v", d, d.ArgoCD)
	}
}

func TestDeployValidationMessages(t *testing.T) {
	cases := []struct {
		name, edit, from, want string
	}{
		{"needs for output", "    needs: build\n    when", "    when", "line 19: steps.deploy-prod: deploy-prod: tag refers to build.tag, but build is not in this step's needs"},
		{"rollback target not deploy", "deployment: deploy-prod", "deployment: smoke", `line 47: steps.rollback-prod.deployment: rollback-prod: deployment "smoke" is not a deploy step`},
		{"rollback must need", "needs: [smoke]", "needs: [build]", "line 48: steps.rollback-prod.needs: rollback-prod: must need deploy-prod, directly or through other steps"},
		{"revision not yet", "      write_back:", "      revision: { run: sha }\n      write_back:", `line 16: steps.deploy-prod.argocd.revision: "revision" is not supported yet`},
		{"until paused needs a rollout step", "until: healthy", "until: paused", "until: paused leaves the rollout at its first pause; add a step with kind: rollout"},
		{"bad pauses", "until: healthy", "until: healthy\n      pauses: sometimes", "write wait (a person promotes from the run page), promote or fail"},
		{"bad on_cancel", "on_cancel: leave", "on_cancel: never", "write leave or abort"},
		{"bad request", "request: auto", "request: sometimes", "write auto (only if the application does not sync by itself), always or never"},
		{"bad windows", "windows: wait", "windows: override", "windows are never overridden"},
		{"no environment", "    environment: prod\n", "", "line 6: steps.deploy-prod.environment: name the environment to deploy to"},
		{"image and set", "            tag: { output: build.tag }", "            tag: { output: build.tag }\n            set: a.b", "a change either sets an image's tag"},
		{"bad image", "image: homelab/beacon", "image: homelab/beacon:1", "line 21: steps.deploy-prod.argocd.write_back.changes[0].image"},
		{"bad path", "set: image.tag", "set: image..tag", "steps.deploy-prod.argocd.write_back.changes[1].set"},
		{"escaping file", "file: apps/beacon/base.yaml", "file: ../secrets.yaml", "must be a path relative to the repository's root"},
		{"run fact", "{ run: sha }", "{ run: author }", `"author" is not a fact about the run`},
		{"script key", "    on_cancel: leave", "    on_cancel: leave\n    image: alpine", "a deploy step runs no script, so it takes no image"},
		{"rollback when", "    timeout: 15m", "    timeout: 15m\n    when: { branch: main }", "a rollback step has no when"},
		{"health resources without", "      health: resources", "      health: resources\n      sync_x: 1", `unknown key "sync_x"`},
		{"group needed", "kind: Rollout, name", "kind: Widget, name", "give the API group of Widget"},
		{"timeout bounds", "timeout: 30m", "timeout: 30s", "timeout must be between 1m and 24h"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(deployFile, tc.edit, tc.from, 1)
			if src == deployFile {
				t.Fatalf("the edit %q did not apply", tc.edit)
			}
			_, err := Parse([]byte(src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v\nwant %s", err, tc.want)
			}
		})
	}
}

func TestTwoDeploysOfOneServiceMustBeOrdered(t *testing.T) {
	src := deployFile + `  deploy-prod-2:
    kind: deploy
    needs: build
    environment: prod
    service: beacon
    argocd:
      application: beacon
      write_back: { repo: gitops, branch: main, changes: [ { file: a.yaml, set: image.tag, value: "1" } ] }
`
	_, err := Parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "line 50: steps.deploy-prod-2: deploy-prod and deploy-prod-2 both deploy beacon to prod and neither needs the other") {
		t.Fatalf("got %v", err)
	}
	// Ordered, they are fine.
	if _, err := Parse([]byte(strings.Replace(src, "    needs: build\n    environment: prod\n    service", "    needs: rollback-prod\n    environment: prod\n    service", 1))); err != nil {
		t.Fatal(err)
	}
}

func TestDeployStepsNotInTemplates(t *testing.T) {
	_, err := readTemplate([]byte(`template: t
steps:
  d:
    kind: deploy
    environment: prod
`), "t", nil, nil, 0, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be in a template yet") {
		t.Fatalf("got %v", err)
	}
}

func TestNestedDeployOfOneServiceIsRefused(t *testing.T) {
	src := strings.Replace(deployFile, "    needs: [smoke]\n", "    needs: [smoke, deploy-again]\n", 1) + `  deploy-again:
    kind: deploy
    needs: smoke
    environment: prod
    service: beacon
    argocd:
      application: beacon
      write_back: { repo: gitops, branch: main, changes: [ { file: a.yaml, set: image.tag, value: "1" } ] }
`
	_, err := Parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "deploy-again deploys to prod inside deployment deploy-prod") {
		t.Fatalf("got %v", err)
	}
}
