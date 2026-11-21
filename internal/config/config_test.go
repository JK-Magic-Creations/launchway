package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

// store is a SharedStore in memory.
type store map[string]string

func (s store) SharedValue(key, proposed string) (string, error) {
	if v, ok := s[key]; ok {
		return v, nil
	}
	s[key] = proposed
	return proposed, nil
}

func TestRunnerToken(t *testing.T) {
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "")
	shared := store{}

	// An existing installation's token is offered to the database, so its
	// runners keep working.
	a := t.TempDir()
	os.WriteFile(TokenFile(a), []byte("old-token\n"), 0o600)
	tok, err := RunnerToken(a, shared)
	if err != nil || tok != "old-token" {
		t.Fatalf("token %q, %v", tok, err)
	}

	// A second server with no token, or a different one, adopts the shared
	// token and mirrors it to its data directory.
	b := t.TempDir()
	if tok, _ := RunnerToken(b, shared); tok != "old-token" {
		t.Fatalf("second server got %q", tok)
	}
	if data, _ := os.ReadFile(TokenFile(b)); strings.TrimSpace(string(data)) != "old-token" {
		t.Fatalf("token file %q", data)
	}

	// A fresh database and data directory get a new random token.
	if tok, _ := RunnerToken(t.TempDir(), store{}); len(tok) != 48 {
		t.Fatalf("new token %q", tok)
	}

	// The environment wins.
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "from-env")
	if tok, _ := RunnerToken(a, shared); tok != "from-env" {
		t.Fatalf("token %q", tok)
	}
}

func TestDatabaseAddr(t *testing.T) {
	t.Setenv("LAUNCHWAY_DATABASE", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte("data: d\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.DatabaseAddr(); got != filepath.Join(dir, "d", "launchway.db") {
		t.Fatalf("default %q", got)
	}
	os.WriteFile(path, []byte("database: postgres://db.example/launchway\n"), 0o644)
	c, _ = Load(path)
	if got := c.DatabaseAddr(); got != "postgres://db.example/launchway" {
		t.Fatalf("configured %q", got)
	}
	os.WriteFile(path, []byte("database: other.db\n"), 0o644)
	c, _ = Load(path)
	if got := c.DatabaseAddr(); got != filepath.Join(dir, "other.db") {
		t.Fatalf("relative %q", got)
	}
	t.Setenv("LAUNCHWAY_DATABASE", "postgres://env/launchway")
	if got := c.DatabaseAddr(); got != "postgres://env/launchway" {
		t.Fatalf("env %q", got)
	}
}

func TestRunnerSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte(`
runner:
  secrets:
    mounted: { type: file, dir: secrets, projects: [web-*] }
    vault:   { type: vault, address: "https://vault.example:8200", role: ci, keys: ["apps/*/*"] }
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Runner.Secrets["mounted"]
	if m.Type != "file" || m.Dir != filepath.Join(dir, "secrets") || m.Projects[0] != "web-*" {
		t.Fatalf("mounted %+v", m)
	}
	if v := c.Runner.Secrets["vault"]; v.Role != "ci" || v.Keys[0] != "apps/*/*" {
		t.Fatalf("vault %+v", v)
	}
	os.WriteFile(path, []byte("runner:\n  secrets:\n    v: { type: vault, adress: x }\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "adress") {
		t.Fatalf("a misspelt setting was accepted: %v", err)
	}
}

func TestApprovalsConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte("approvals:\n  allow_self: false\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Approvals.AllowSelf == nil || *c.Approvals.AllowSelf {
		t.Fatalf("allow_self %v", c.Approvals.AllowSelf)
	}
	c, err = Load(filepath.Join(dir, "missing.yaml"))
	if err != nil || c.Approvals.AllowSelf != nil {
		t.Fatalf("default %v %v", c.Approvals.AllowSelf, err)
	}
}

func TestDraftsAndPushSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte(`
drafts:
  allow: admin
projects:
  - name: web
    repo: ./web
    push: { branch_prefix: "edits/", allow_direct: ["main", "release/*"] }
  - name: pay
    repo: ./pay
    drafts: { allow: operator, secrets: true }
  - name: docs
    repo: ./docs
    drafts: { allow: none }
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := c.DraftRules()
	if err != nil {
		t.Fatal(err)
	}
	for project, want := range map[string]auth.DraftRule{
		"web":   {Allow: auth.Admin},
		"pay":   {Allow: auth.Operator, Secrets: true},
		"docs":  {Allow: auth.NoRole},
		"other": {Allow: auth.Admin},
	} {
		if got := rules.For(project); got != want {
			t.Errorf("%s: %+v, want %+v", project, got, want)
		}
	}
	projects, err := c.SourceProjects()
	if err != nil {
		t.Fatal(err)
	}
	if p := projects[0].Push; p == nil || p.BranchPrefix != "edits/" || strings.Join(p.AllowDirect, ",") != "main,release/*" || projects[1].Push != nil {
		t.Fatalf("push %+v", projects)
	}

	// Without a setting: operators, no secrets.
	empty, _ := Load(filepath.Join(dir, "missing.yaml"))
	if r, _ := empty.DraftRules(); r.For("x") != auth.DefaultDraftRule || auth.DefaultDraftRule != (auth.DraftRule{Allow: auth.Operator}) {
		t.Fatalf("default %+v", r.For("x"))
	}

	for _, bad := range []string{"drafts: { allow: viewer }\n", "drafts: { allow: everyone }\n",
		"projects: [{ name: a, repo: ./a, drafts: { allow: sometimes } }]\n", "projects: [{ name: a, repo: ./a, push: { token: x } }]\n"} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, err := Load(path); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestLibrariesAndProjectPipelines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte(`
libraries:
  - name: ci-templates
    repo: ../ci-templates
  - name: remote
    repo: https://git.example.com/platform/templates.git
projects:
  - name: svc
    repo: https://git.example.com/team/svc.git
    pipeline:
      library: ci-templates
      version: v3
      template: python-service
      with: { scan: false, retries: 2, test_command: "pytest -q" }
      on: { push: { branches: [main, develop] } }
  - name: plain
    repo: https://git.example.com/team/plain.git
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	libs := c.SourceLibraries()
	if len(libs) != 2 || libs[0].URL != filepath.Join(filepath.Dir(dir), "ci-templates") || libs[1].URL != "https://git.example.com/platform/templates.git" {
		t.Fatalf("libraries %+v", libs)
	}
	ps, err := c.SourceProjects()
	if err != nil {
		t.Fatal(err)
	}
	pp := ps[0].Pipeline
	if pp == nil || pp.Library != "ci-templates" || pp.Version != "v3" || pp.Template != "python-service" ||
		pp.With["scan"] != "false" || pp.With["retries"] != "2" || pp.With["test_command"] != "pytest -q" ||
		pp.On.Push == nil || strings.Join(pp.On.Push.Branches, ",") != "main,develop" {
		t.Fatalf("pipeline %+v", pp)
	}
	if ps[1].Pipeline != nil {
		t.Fatalf("plain has a pipeline: %+v", ps[1].Pipeline)
	}
	// Unknown settings are refused, as everywhere in the file.
	os.WriteFile(path, []byte("projects:\n  - name: x\n    repo: y\n    pipeline: { library: a, versoin: v1 }\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "versoin") {
		t.Fatalf("err %v", err)
	}
}

// Helm reads values as YAML 1.1 and renders an unquoted `on` as "true".
func TestUnquotedOnFromHelmIsUnderstood(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launchway.yaml")
	os.WriteFile(path, []byte(`
libraries:
  - name: lib
    repo: https://git.example.com/lib.git
projects:
  - name: svc
    repo: https://git.example.com/svc.git
    pipeline:
      library: lib
      version: v1
      template: ci
      "true": { push: { branches: [main] } }
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	pp := c.Projects[0].Pipeline
	if pp.On.Push == nil || len(pp.On.Push.Branches) != 1 || pp.On.Push.Branches[0] != "main" {
		t.Fatalf("on = %+v", pp.On)
	}
}

// Pull requests: a configured pipeline may ask for them and supersede, and
// whether their steps may read secrets is off unless the server or the
// project says otherwise.
func TestPullRequestSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launchway.yaml")
	os.WriteFile(path, []byte(`
libraries:
  - name: lib
    repo: https://git.example.com/lib.git
projects:
  - name: a
    repo: https://git.example.com/a.git
    pipeline:
      library: lib
      version: v1
      template: ci
      supersede: true
      on: { push: { branches: [main] }, pull_request: {} }
  - name: b
    repo: https://git.example.com/b.git
    pull_requests: { secrets: true }
    pipeline:
      library: lib
      version: v1
      template: ci
      "true": { pull_request: }
  - name: c
    repo: https://git.example.com/c.git
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := c.SourceProjects()
	if err != nil {
		t.Fatal(err)
	}
	a, b := ps[0].Pipeline, ps[1].Pipeline
	if !a.Supersede || a.On.PullRequest == nil || a.On.Push == nil || b.Supersede || b.On.PullRequest == nil || b.On.Push != nil {
		t.Fatalf("pipelines %+v %+v", a, b)
	}
	if ps[0].PullRequestSecrets || !ps[1].PullRequestSecrets || ps[2].PullRequestSecrets {
		t.Fatalf("secrets: %v %v %v", ps[0].PullRequestSecrets, ps[1].PullRequestSecrets, ps[2].PullRequestSecrets)
	}

	// The server's setting applies where a project has none.
	os.WriteFile(path, []byte("pull_requests: { secrets: true }\nprojects:\n  - name: a\n    repo: r\n  - name: b\n    repo: r\n    pull_requests: { secrets: false }\n"), 0o644)
	if c, err = Load(path); err != nil {
		t.Fatal(err)
	}
	if ps, _ = c.SourceProjects(); !ps[0].PullRequestSecrets || ps[1].PullRequestSecrets {
		t.Fatalf("secrets: %v %v", ps[0].PullRequestSecrets, ps[1].PullRequestSecrets)
	}

	// Pull requests take no settings; there is no branch to filter on.
	os.WriteFile(path, []byte("projects:\n  - name: a\n    repo: r\n    pipeline: { library: l, version: v1, template: t, on: { pull_request: { branches: [main] } } }\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "takes no settings") {
		t.Fatalf("err %v", err)
	}
	os.WriteFile(path, []byte("projects:\n  - name: a\n    repo: r\n    pull_requests: { secret: true }\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("err %v", err)
	}
}

func TestTestReportSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte(`
tests: { max_upload_mib: 8, max_cases_per_run: 1000, max_cases_per_project: 5000 }
runner:
  reports: { max_files: 5, max_file_mib: 2, max_total_mib: 4 }
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	tl, err := c.TestLimits()
	if err != nil || tl.MaxUploadBytes != 8<<20 || tl.MaxCasesPerRun != 1000 || tl.MaxCasesPerProject != 5000 {
		t.Fatalf("test limits %+v %v", tl, err)
	}
	rl, err := c.Runner.Reports.Limits()
	if err != nil || rl.MaxFiles != 5 || rl.MaxFileBytes != 2<<20 || rl.MaxBytes != 4<<20 {
		t.Fatalf("report limits %+v %v", rl, err)
	}
	// Left out, the defaults.
	c, _ = Load(filepath.Join(dir, "missing.yaml"))
	if rl, _ := c.Runner.Reports.Limits(); rl.MaxFiles != 100 || rl.MaxFileBytes != 10<<20 || rl.MaxBytes != 20<<20 {
		t.Fatalf("default report limits %+v", rl)
	}
	for _, bad := range []string{"tests: { max_upload_mib: -1 }\n", "tests: { max_upload_mib: 5000 }\n",
		"runner: { reports: { max_files: -3 } }\n", "runner: { reports: { max_file_mb: 3 } }\n"} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, err := Load(path); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRunnerPools(t *testing.T) {
	dir := t.TempDir()
	tok := strings.Repeat("a", 40)
	os.WriteFile(filepath.Join(dir, "pool-token"), []byte(tok+"\n"), 0o600)
	t.Setenv("LW_TEST_POOL", strings.Repeat("b", 40))
	write := func(body string) string {
		p := filepath.Join(dir, "launchway.yaml")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	base := "environments:\n  - { name: prod, target: cluster-a, projects: [shop] }\n"
	c, err := Load(write(base + "runner_pools:\n  - { name: a, token_file: pool-token, targets: [cluster-a] }\n  - { name: b, token_env: LW_TEST_POOL, targets: [cluster-a] }\n"))
	if err != nil {
		t.Fatal(err)
	}
	pools, err := c.Pools("shared")
	if err != nil || len(pools) != 2 || pools[0].Token != tok || pools[1].Token != strings.Repeat("b", 40) {
		t.Fatalf("pools: %+v %v", pools, err)
	}
	if envs := c.EngineEnvironments(); len(envs) != 1 || envs[0].Target != "cluster-a" {
		t.Fatalf("environments: %+v", envs)
	}
	for body, want := range map[string]string{
		"runner_pools:\n  - { name: a, token_env: LW_TEST_POOL, targets: [elsewhere] }\n":                                                                 "no environment has target",
		"runner_pools:\n  - { name: a, token_env: LW_NOT_SET, targets: [cluster-a] }\n":                                                                   "is not set",
		"runner_pools:\n  - { name: a, targets: [cluster-a] }\n":                                                                                          "exactly one of token_env or token_file",
		"runner_pools:\n  - { name: a, token_env: LW_TEST_POOL, targets: [cluster-a] }\n  - { name: b, token_env: LW_TEST_POOL, targets: [cluster-a] }\n": "also the token of pool a",
	} {
		c, err := Load(write(base + body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Pools("shared"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", body, err, want)
		}
	}
	c, _ = Load(write(base + "runner_pools:\n  - { name: a, token_env: LW_TEST_POOL, targets: [cluster-a] }\n"))
	if _, err := c.Pools(strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "shared runner token") {
		t.Errorf("a pool with the shared token: %v", err)
	}
}

func TestDeployRunnerFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "runner.yaml")
	os.WriteFile(p, []byte(`server: http://x
deploy:
  targets:
    cluster-a:
      kubernetes: { kubeconfig: kube.yaml, context: kind-launchway-test }
      applications: [ { projects: [shop], names: [shop] } ]
  repos:
    gitops:
      url: http://git.example/gitops.git
      credentials: { store: vault, key: ci/gitops, field: token }
      paths: [ { projects: [shop], files: ["apps/**"] } ]
`), 0o644)
	f, err := LoadRunner(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.Deploy.Targets["cluster-a"].Kubernetes.Kubeconfig != filepath.Join(dir, "kube.yaml") || f.Deploy.Targets["cluster-a"].ArgoCDNamespace != "argocd" {
		t.Fatalf("deploy: %+v", f.Deploy)
	}
	// A target must name its cluster: the current context is never used.
	os.WriteFile(p, []byte("deploy:\n  targets:\n    cluster-a:\n      applications: [ { projects: [shop], names: [shop] } ]\n"), 0o644)
	if _, err := LoadRunner(p); err == nil || !strings.Contains(err.Error(), "the current context is never used") {
		t.Fatalf("got %v", err)
	}
}

// Policies in the server's configuration are checked when it is read, and
// a mistake is refused with the file and the line.
func TestPolicyConfiguration(t *testing.T) {
	cases := []struct{ body, want string }{
		{"policy:\n  policies:\n    - name: a\n      mode: enforce\n      rules:\n        tests: { min: 0 }\n", "c.yaml:6: policy.policies[0].rules.tests.min: must be between 1"},
		{"policy:\n  policies:\n    - name: a\n      mode: enforce\n      rules:\n        friday: {}\n", `c.yaml:6: policy.policies[0].rules.friday: unknown rule "friday"`},
		{"policy:\n  sources: [ { name: org, library: nope, version: v1 } ]\n", `policy.sources: org: library "nope" is not one of libraries:`},
		{"policy:\n  waivers: { allow: everyone }\n", "policy.waivers.allow"},
		{"policy:\n  colour: red\n", "field colour not found"},
	}
	for _, c := range cases {
		err := loadFile(t, c.body, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.body, err)
		}
	}
	if err := loadFile(t, "policy:\n  policies:\n    - { name: a, mode: report, rules: { max_timeout: 2h } }\n", false); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPoolsAndRunnerAdmin(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "launchway.yaml")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	base := "environments:\n  - { name: prod, target: cluster-a, projects: [shop] }\nrunner: { enabled: false }\n"
	c, err := Load(write(base + `runner_admin: operator
runner_pools:
  - { name: default, kind: build, projects: [shop], require_identity: true }
  - { name: web, kind: build, projects: ["web-*"], events: [push, manual] }
  - { name: deployers, targets: [cluster-a], require_identity: true }
`))
	if err != nil {
		t.Fatal(err)
	}
	pools, err := c.Pools("shared")
	if err != nil || len(pools) != 3 || pools[1].Kind != "build" || pools[1].Events[1] != "manual" || pools[2].Kind != "deploy" ||
		pools[2].Token != "" || !pools[2].RequireIdentity {
		t.Fatalf("%+v %v", pools, err)
	}
	if ep := EnginePools(pools); len(ep) != 3 || ep[0].Name != "default" || !ep[0].RequireIdentity {
		t.Fatalf("%+v", ep)
	}
	if r, err := c.RunnerAdminRole(); err != nil || r.String() != "operator" {
		t.Fatalf("%v %v", r, err)
	}
	for body, want := range map[string]string{
		"runner_pools:\n  - { name: web, kind: build }\n":                                              "list the projects",
		"runner_pools:\n  - { name: web, kind: build, projects: [a], targets: [cluster-a] }\n":         "no targets and no token",
		"runner_pools:\n  - { name: web, kind: gpu }\n":                                                "kind is deploy",
		"runner_pools:\n  - { name: d, targets: [cluster-a], projects: [a], token_env: X }\n":          "for build pools",
		"runner_pools:\n  - { name: default, targets: [cluster-a], token_env: X }\n":                   "give the deploy pool another name",
		"runner_pools:\n  - { name: d, targets: [cluster-a], token_env: X, require_identity: true }\n": "give it none",
	} {
		c, err := Load(write(base + body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Pools("shared"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %s", body, err, want)
		}
	}
	// The built-in runner uses the shared token.
	c, _ = Load(write("runner_pools:\n  - { name: default, kind: build, require_identity: true }\n"))
	if _, err := c.Pools("shared"); err == nil || !strings.Contains(err.Error(), "built-in runner") {
		t.Errorf("built-in runner with require_identity: %v", err)
	}
	if _, err := Load(write("runner_admin: everyone\n")); err == nil || !strings.Contains(err.Error(), "runner_admin") {
		t.Errorf("bad runner_admin: %v", err)
	}
}

func TestRunnerFileAcceptAndProxy(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "runner.yaml")
	os.WriteFile(p, []byte(`server: https://ci.example.com
labels: [arm64]
proxy: http://proxy.example.com:3128
no_proxy: internal.example.com
ca_file: certs/ca.pem
accept:
  projects: [shop, "web-*"]
  repositories: ["https://git.example.com/shop/*"]
  images: ["registry.example.com/*"]
  images_by_digest: true
  executors: [podman]
  pull_requests: false
  drafts: false
  max_timeout: 2h
  env_deny: [LD_PRELOAD, "GIT_*"]
`), 0o644)
	f, err := LoadRunner(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.CAFile != filepath.Join(dir, "certs/ca.pem") || f.Proxy == "" || f.Labels[0] != "arm64" || f.Accept == nil ||
		*f.Accept.PullRequests || f.Accept.Timeout().Hours() != 2 {
		t.Fatalf("%+v %+v", f, f.Accept)
	}
	os.WriteFile(p, []byte("server: x\naccept: { max_timeout: soon }\n"), 0o644)
	if _, err := LoadRunner(p); err == nil || !strings.Contains(err.Error(), "accept.max_timeout") {
		t.Fatalf("bad accept: %v", err)
	}
}
