package pipeline

import (
	"os"
	"path/filepath"
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

func exampleLibrary(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "examples", "library", "templates")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(e.Name(), ".yaml")] = string(b)
	}
	return out
}

func TestServiceCDExample(t *testing.T) {
	src := `pipeline: shop
libraries:
  platform: { repo: platform, version: v1 }
steps:
  dev:
    uses: platform/service-cd
    when: { branch: develop }
    with:
      environment: dev
      application: shop-dev
      image_repository: team/shop
      manifest: apps/shop-dev/deployment.yaml
      workload: shop
      namespace: shop-dev
      health_query: 'min(probe_success{instance="http://shop.shop-dev.svc:8080/health"})'
  prod:
    uses: platform/service-cd
    needs: dev
    when: { branch: main }
    with:
      environment: prod
      application: shop
      image_repository: team/shop
      manifest: apps/shop/deployment.yaml
      workload: shop
      namespace: shop
      approve: true
      scan_mode: enforce
      sign: true
      cache_registry: registry
      health_query: 'min(probe_success{instance="http://shop.shop.svc:8080/health"})'
`
	p := mustExpand(t, src, exampleLibrary(t))
	for _, env := range []string{"dev", "prod"} {
		d := p.Step(env + "/deploy")
		if d == nil || d.Environment != env {
			t.Fatalf("%s: %v", env, ids(p))
		}
		if r := p.RollbackOf(d.ID); r == nil {
			t.Fatalf("%s has no rollback", env)
		}
		if c := d.ArgoCD.WriteBack.Changes[0]; c.Digest == nil || c.Digest.Output.Step != env+"/image" {
			t.Fatalf("%s change: %+v", env, c)
		}
	}
	if p.Step("dev/approve") != nil || p.Step("prod/approve") == nil {
		t.Fatal("only prod asks")
	}
	img := p.Step("prod/image").ImageBuild
	if img.Scan.Mode != "enforce" || !img.Sign.Runner || img.Cache == nil || img.Cache.Registry != "registry" {
		t.Fatalf("prod image: %+v %+v %+v", img.Scan, img.Sign, img.Cache)
	}
	if p.Step("dev/image").ImageBuild.Cache != nil || p.Step("dev/image").ImageBuild.Sign != nil {
		t.Fatal("dev image should have neither cache nor signature")
	}
	if m := p.Step("prod/verify").Verify.Metrics[0]; m.Min == nil || *m.Min != 1 || !strings.Contains(m.Query, "shop.shop.svc") || m.Source != "prometheus" {
		t.Fatalf("metric: %+v", m)
	}
	if g := p.Group("prod"); g.Outputs["digest"].Step != "prod/image" {
		t.Fatalf("outputs %v", g.Outputs)
	}
}

func TestServiceCDRolloutExample(t *testing.T) {
	src := `pipeline: shop
libraries:
  platform: { repo: platform, version: v1 }
steps:
  prod:
    uses: platform/service-cd-rollout
    with:
      environment: prod
      application: shop
      image_repository: team/shop
      manifest: apps/shop/rollout.yaml
      workload: shop
      namespace: shop
      pauses: promote
      health_query: 'min(probe_success{instance="http://shop.shop.svc:8080/health"})'
`
	p := mustExpand(t, src, exampleLibrary(t))
	d := p.Step("prod/deploy")
	if d.Rollout == nil || d.Rollout.Name != "shop" || d.Rollout.Namespace != "shop" || d.Rollout.Pauses != "promote" {
		t.Fatalf("rollout: %+v", d.Rollout)
	}
	if r := d.ArgoCD.Sync.Resources[0]; r.Kind != "Rollout" || r.Group != "argoproj.io" {
		t.Fatalf("resource: %+v", r)
	}
	if p.Requires < SchemaControl {
		t.Fatalf("requires %d", p.Requires)
	}
}

func TestServiceCIExample(t *testing.T) {
	src := `pipeline: shop
libraries:
  platform: { repo: platform, version: v1 }
steps:
  backend:
    uses: platform/service-ci-postgres
    with: { database: shop_test, working_directory: backend, coverage_minimum: 80, install_command: "pip install -e .[dev]" }
  app:
    uses: platform/service-ci
    with: { language: node, working_directory: app }
  lib:
    uses: platform/service-ci
    with: { code_scan: false, scan_mode: report }
`
	p := mustExpand(t, src, exampleLibrary(t))
	test := p.Step("backend/checks/python-test")
	if test == nil {
		t.Fatalf("steps %v", ids(p))
	}
	if test.Env["DATABASE_URL"] != "postgres://postgres:test@localhost:5432/shop_test" || test.Env["PARAM_WORKING_DIRECTORY"] != "backend" {
		t.Fatalf("env %v", test.Env)
	}
	if test.Reports == nil || test.Reports.CoverageFailUnder == nil || test.Reports.CoverageFailUnder.Lines != 80 || len(test.Reports.Tests) != 1 {
		t.Fatalf("reports %+v", test.Reports)
	}
	if !contains(test.Needs, "backend/checks/python-deps") {
		t.Fatalf("needs %v", test.Needs)
	}
	deps := p.Step("backend/checks/python-deps")
	if !contains(deps.Needs, "backend/database-ready") {
		t.Fatalf("the checks must wait for the database: %v", deps.Needs)
	}
	if len(deps.Cache) != 1 || deps.Cache[0].Name != "pip" {
		t.Fatalf("cache %+v", deps.Cache)
	}
	if p.Step("app/node-test") == nil || p.Step("app/python-test") != nil {
		t.Fatalf("language: node should keep only the node steps: %v", ids(p))
	}
	if p.Step("lib/python-code-scan") != nil {
		t.Fatal("code_scan: false should leave the scan out")
	}
	if p.Step("lib/python-test").Reports.CoverageFailUnder != nil {
		t.Fatal("no threshold by default")
	}
	var db *Container
	for _, c := range p.Containers {
		if c.Service {
			db = c
		}
	}
	if db == nil || db.Env["POSTGRES_DB"] != "shop_test" {
		t.Fatalf("service %+v", db)
	}
}

// The thin pipeline files of the example library expand against its
// templates and come out as complete graphs.
func TestExamplePipelines(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "library", "pipelines")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("%v %d", err, len(entries))
	}
	lib := exampleLibrary(t)
	wantDeploys := map[string][]string{
		"small-service":   {"dev/deploy", "prod/deploy"},
		"rollout-service": {"prod/deploy"},
		"monorepo":        {"deliver/deploy"},
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".yaml")
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			p := mustExpand(t, string(b), lib)
			for _, id := range wantDeploys[name] {
				if d := p.Step(id); d == nil || !d.IsDeploy() || p.RollbackOf(id) == nil {
					t.Errorf("%s: no deploy with a rollback in %v", id, ids(p))
				}
			}
			if len(p.Steps) < 5 || len(p.Steps) > 30 {
				t.Errorf("%d steps", len(p.Steps))
			}
			// The pipeline file stays short: that is what the library is for.
			if lines := strings.Count(string(b), "\n"); lines > 60 {
				t.Errorf("the file is %d lines", lines)
			}
		})
	}
}

// Two services of one project in one environment (a back end and a front end)
// give the delivery template different service names and run side by side;
// without names they would both be the project and must be ordered.
func TestServiceCDTwoServicesInOneEnvironment(t *testing.T) {
	use := func(id, service string) string {
		s := "  " + id + ":\n    uses: platform/service-cd-rollout\n    with:\n      environment: prod\n      application: shop\n" +
			"      image_repository: team/" + id + "\n      manifest: apps/shop/rollouts.yaml\n      workload: " + id + "\n      namespace: shop\n" +
			"      health_query: 'min(up)'\n"
		if service != "" {
			s += "      service: " + service + "\n"
		}
		return s
	}
	head := "pipeline: shop\nlibraries:\n  platform: { repo: platform, version: v1 }\nsteps:\n"
	p := mustExpand(t, head+use("backend", "shop-backend")+use("frontend", "shop-frontend"), exampleLibrary(t))
	if p.Step("backend/deploy").Service != "shop-backend" || p.Step("frontend/deploy").Service != "shop-frontend" {
		t.Fatal("service names lost")
	}
	_, err := expand(t, head+use("backend", "")+use("frontend", ""), exampleLibrary(t))
	if err == nil || !strings.Contains(err.Error(), "neither needs the other") {
		t.Fatalf("got %v", err)
	}
}
