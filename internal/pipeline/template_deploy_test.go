package pipeline

import (
	"strings"
	"testing"
)

// cdTemplate is a delivery template: the image, the deploy, the check of
// the deployment, the person who decides, and the way back.
const cdTemplate = `template: cd
description: Build, deploy, verify and roll back
params:
  environment: { type: environment }
  application: { type: application }
  repository:  { type: string }
  approve:     { type: boolean, default: false }
steps:
  tag:
    image: alpine:3.22
    run: echo tag=1 >> "$LAUNCHWAY_OUTPUTS"
  image:
    kind: image
    needs: tag
    tags: [ { registry: harbor, repository: { param: repository }, tag: { output: tag.tag } } ]
  approve:
    kind: approval
    if: { param: approve }
    needs: image
    message: Deploy to production?
  deploy:
    kind: deploy
    needs: [image, approve]
    environment: { param: environment }
    version: { output: tag.tag }
    argocd:
      application: { param: application }
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: { param: application, format: "apps/%s/base.yaml" }, image: { param: repository }, tag: { output: tag.tag } }
          - { file: x.yaml, set: image.digest, value: { output: image.digest } }
      sync:
        request: always
        resources: [ { kind: Deployment, name: { param: application }, namespace: apps } ]
  verify:
    kind: verify
    needs: deploy
    deployment: deploy
    duration: 1m
    metrics:
      - { name: probe, source: prometheus, min: 1, query: 'min(probe_success{app="{{app}}"})' }
    params: { app: { param: application } }
  rollback:
    kind: rollback
    needs: verify
    deployment: deploy
outputs:
  tag: { from: tag.tag }
`

const cdPipeline = `pipeline: svc
libraries:
  platform: { repo: shared, version: v1 }
steps:
  prod:
    uses: platform/cd
    with: { environment: prod, application: svc, repository: team/svc, approve: true }
`

func TestTemplateMayDeployVerifyAndRollBack(t *testing.T) {
	p := mustExpand(t, cdPipeline, map[string]string{"cd": cdTemplate})
	deploy := p.Step("prod/deploy")
	if deploy == nil || !deploy.IsDeploy() {
		t.Fatalf("no deploy step in %v", ids(p))
	}
	if deploy.Environment != "prod" || deploy.ArgoCD.Application != "svc" {
		t.Fatalf("environment %q application %q", deploy.Environment, deploy.ArgoCD.Application)
	}
	ch := deploy.ArgoCD.WriteBack.Changes
	if ch[0].File != "apps/svc/base.yaml" || ch[0].Image != "team/svc" || ch[0].Tag.Output.Step != "prod/tag" {
		t.Fatalf("change 0: %+v", ch[0])
	}
	if ch[1].Value.Output.Step != "prod/image" || deploy.Version.Output.Step != "prod/tag" {
		t.Fatalf("outputs not prefixed: %+v %+v", ch[1].Value.Output, deploy.Version.Output)
	}
	if got := deploy.ArgoCD.Sync.Resources[0].Name; got != "svc" {
		t.Fatalf("resource %q", got)
	}
	if v := p.Step("prod/verify"); v == nil || v.Deployment != "prod/deploy" || v.Verify.Params["app"].Literal != "svc" {
		t.Fatalf("verify: %+v", v)
	}
	if r := p.RollbackOf("prod/deploy"); r == nil || r.ID != "prod/rollback" {
		t.Fatalf("rollback: %v", r)
	}
	a := p.Step("prod/approve")
	if a == nil || !a.IsApproval() {
		t.Fatalf("approval missing: %v", ids(p))
	}
	if !contains(deploy.Needs, "prod/approve") {
		t.Fatalf("deploy needs %v", deploy.Needs)
	}
	if p.Requires < SchemaVerify {
		t.Fatalf("requires %d, want at least %d", p.Requires, SchemaVerify)
	}
}

func TestTemplateWithoutApprovalDeploysDirectly(t *testing.T) {
	p := mustExpand(t, strings.Replace(cdPipeline, "approve: true", "approve: false", 1), map[string]string{"cd": cdTemplate})
	if p.Step("prod/approve") != nil {
		t.Fatal("approval kept")
	}
	if got := p.Step("prod/deploy").Needs; len(got) != 1 || got[0] != "prod/image" {
		t.Fatalf("deploy needs %v", got)
	}
}

func TestTwoUsesDeployToTwoEnvironments(t *testing.T) {
	src := cdPipeline + `  dev:
    uses: platform/cd
    when: { branch: develop }
    with: { environment: dev, application: svc-dev, repository: team/svc }
`
	p := mustExpand(t, src, map[string]string{"cd": cdTemplate})
	if p.Step("dev/deploy").Environment != "dev" || p.Step("prod/deploy").Environment != "prod" {
		t.Fatal("environments mixed up")
	}
	if p.Step("dev/verify").Deployment != "dev/deploy" {
		t.Fatalf("dev verify names %q", p.Step("dev/verify").Deployment)
	}
}

func TestTwoUsesOfOneEnvironmentMustBeOrdered(t *testing.T) {
	src := cdPipeline + `  again:
    uses: platform/cd
    with: { environment: prod, application: svc, repository: team/svc }
`
	_, err := expand(t, src, map[string]string{"cd": cdTemplate})
	if err == nil || !strings.Contains(err.Error(), "both deploy") {
		t.Fatalf("got %v", err)
	}
}

func TestEnvironmentParameterIsTyped(t *testing.T) {
	for _, tc := range []struct{ with, want string }{
		{"environment: Prod, application: svc, repository: a/b", "name of an environment"},
		{"environment: prod, application: Svc_1, repository: a/b", "name of an Argo CD application"},
		{"application: svc, repository: a/b", "needs a value for environment"},
	} {
		src := "pipeline: p\nlibraries:\n  platform: { repo: shared, version: v1 }\nsteps:\n  cd:\n    uses: platform/cd\n    with: { " + tc.with + " }\n"
		_, err := expand(t, src, map[string]string{"cd": cdTemplate})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.with, err, tc.want)
		}
	}
}

func TestEnvironmentComesFromARunInput(t *testing.T) {
	src := `pipeline: p
libraries:
  platform: { repo: shared, version: v1 }
inputs:
  target: { type: choice, options: [dev, prod], default: dev }
steps:
  cd:
    uses: platform/cd
    with: { environment: { input: target }, application: svc, repository: a/b }
`
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	p, err = Bind(p, map[string]string{"target": "prod"}, BindOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range p.Libraries {
		l.Commit = testCommit
	}
	out, err := Expand(p, func(*Library, string) ([]byte, error) { return []byte(cdTemplate), nil })
	if err != nil {
		t.Fatal(err)
	}
	if out.Step("cd/deploy").Environment != "prod" {
		t.Fatalf("environment %q", out.Step("cd/deploy").Environment)
	}
}

func TestTemplateDeployStepsAreCheckedLikeAPipelinesOwn(t *testing.T) {
	bad := strings.Replace(cdTemplate, "    deployment: deploy\n", "    deployment: nowhere\n", 1)
	_, err := expand(t, cdPipeline, map[string]string{"cd": bad})
	if err == nil || !strings.Contains(err.Error(), "there is no step") {
		t.Fatalf("got %v", err)
	}
	noInput := strings.Replace(cdTemplate, "environment: { param: environment }", "environment: { input: target }", 1)
	_, err = expand(t, cdPipeline, map[string]string{"cd": noInput})
	if err == nil || !strings.Contains(err.Error(), "cannot read run inputs") {
		t.Fatalf("got %v", err)
	}
}

func TestPipelineStepMayNeedTemplateDeployment(t *testing.T) {
	src := cdPipeline + `  announce:
    image: alpine:3.22
    needs: prod
    run: echo done
`
	p := mustExpand(t, src, map[string]string{"cd": cdTemplate})
	n := p.Step("announce").Needs
	if !contains(n, "prod/rollback") && !contains(n, "prod/deploy") {
		t.Fatalf("announce needs %v", n)
	}
}

func ids(p *Pipeline) []string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.ID)
	}
	return out
}

func TestTemplateDeclaresReportsOnceForEveryUse(t *testing.T) {
	tmpl := `template: py
params:
  tests:    { type: string, default: "reports/unit/*.xml" }
  coverage: { type: string, default: "coverage.xml" }
  minimum:  { type: number, default: "0" }
steps:
  test:
    image: python:3.13
    run: python -m pytest --junitxml=reports/unit/junit.xml --cov --cov-report=xml
    reports:
      tests: { param: tests }
      coverage: { paths: { param: coverage }, fail_under: { param: minimum } }
      findings: reports/security/*.sarif
`
	use := func(with string) *Pipeline {
		return mustExpand(t, "pipeline: p\nlibraries:\n  l: { repo: l, version: v1 }\nsteps:\n  ci: { uses: l/py"+with+" }\n", map[string]string{"py": tmpl})
	}
	s := use("").Step("ci/test")
	if s.Reports == nil || s.Reports.Tests[0] != "reports/unit/*.xml" || s.Reports.Coverage[0] != "coverage.xml" || s.Reports.CoverageFailUnder != nil {
		t.Fatalf("defaults: %+v", s.Reports)
	}
	s = use(", with: { minimum: 75, tests: 'out/*.xml' }").Step("ci/test")
	if s.Reports.CoverageFailUnder == nil || s.Reports.CoverageFailUnder.Lines != 75 || s.Reports.Tests[0] != "out/*.xml" || len(s.Reports.Findings) != 1 {
		t.Fatalf("with: %+v", s.Reports)
	}
	if _, err := expand(t, "pipeline: p\nlibraries:\n  l: { repo: l, version: v1 }\nsteps:\n  ci: { uses: l/py, with: { minimum: 150 } }\n", map[string]string{"py": tmpl}); err == nil ||
		!strings.Contains(err.Error(), "percentage") {
		t.Fatalf("got %v", err)
	}
}
