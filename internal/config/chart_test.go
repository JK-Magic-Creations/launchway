package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
	hsource "github.com/jk-magic-creations/launchway/internal/verify/source"
)

// renderChart renders the Helm chart with values and returns its ConfigMaps'
// files by ConfigMap name suffix (server, runner, deploy-runner).
func renderChart(t *testing.T, values string) map[string]string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	vf := filepath.Join(t.TempDir(), "values.yaml")
	os.WriteFile(vf, []byte(values), 0o644)
	out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", vf).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	files := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var doc struct {
			Kind     string
			Metadata struct{ Name string }
			Data     map[string]string
		}
		if err := dec.Decode(&doc); err != nil {
			break
		}
		if doc.Kind == "ConfigMap" {
			for _, v := range doc.Data {
				files[strings.TrimPrefix(doc.Metadata.Name, "t-launchway-")] = v
			}
		}
	}
	return files
}

func loadFile(t *testing.T, body string, runner bool) error {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte(body), 0o644)
	if runner {
		_, err := LoadRunner(p)
		return err
	}
	_, err := Load(p)
	return err
}

// With the chart's default values, the configuration has none of the
// delivery settings (so a release that does not know them can read it);
// with them set, every rendered file loads.
func TestChartDeliverySettings(t *testing.T) {
	def := renderChart(t, "postgresql: { enabled: true }\n")
	for _, key := range []string{"environments", "runner_pools", "deploy:"} {
		for name, f := range def {
			if strings.Contains(f, key) {
				t.Errorf("the default %s configuration has %s:\n%s", name, key, f)
			}
		}
	}
	files := renderChart(t, `postgresql: { enabled: true }
delivery:
  environments:
    - { name: prod, target: homelab, projects: [shop] }
  deployRunner:
    enabled: true
    target: homelab
    applications: [ { projects: [shop], names: [shop] } ]
    rolloutNamespaces: [shop]
    repos:
      gitops:
        url: http://git.example/gitops.git
        paths: [ { projects: [shop], files: ["apps/shop/**"] } ]
`)
	t.Setenv("LAUNCHWAY_DEPLOY_POOL_TOKEN", strings.Repeat("x", 48))
	if err := loadFile(t, files["server"], false); err != nil {
		t.Fatalf("server: %v\n%s", err, files["server"])
	}
	c, _ := Load(writeTemp(t, files["server"]))
	if pools, err := c.Pools("shared"); err != nil || len(pools) != 1 || pools[0].Name != "deploy-homelab" {
		t.Fatalf("pools: %+v %v", pools, err)
	}
	if err := loadFile(t, files["deploy-runner"], true); err != nil {
		t.Fatalf("deploy runner: %v\n%s", err, files["deploy-runner"])
	}
	if err := loadFile(t, files["runner"], true); err != nil {
		t.Fatalf("runner: %v", err)
	}
}

func writeTemp(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

// Rollout control in the chart: nothing by default (no status patch right,
// no policy, and no new setting in any configuration file); with
// rbac.controlRollouts, a status-only patch right on exactly the named
// rollouts, and the admission policy holding the patch to the fields that
// promote, abort and retry set.
func TestChartRolloutControl(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	render := func(values string) string {
		vf := filepath.Join(t.TempDir(), "values.yaml")
		os.WriteFile(vf, []byte(values), 0o644)
		out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", vf).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template: %v\n%s", err, out)
		}
		return string(out)
	}
	base := `postgresql: { enabled: true }
delivery:
  environments:
    - { name: prod, target: homelab, projects: [shop] }
  deployRunner:
    enabled: true
    target: homelab
    applications: [ { projects: [shop], names: [shop] } ]
    rolloutNamespaces: [shop]
    repos:
      gitops:
        url: http://git.example/gitops.git
        paths: [ { projects: [shop], files: ["apps/shop/**"] } ]
`
	for _, out := range []string{render("postgresql: { enabled: true }\n"), render(base)} {
		if strings.Contains(out, "rollouts/status") || strings.Contains(out, "protected") {
			t.Fatalf("rendered rollout control without being asked:\n%s", out)
		}
	}
	out := render(base + "    rbac: { controlRollouts: { shop: [shop] } }\n")
	if !strings.Contains(out, "resources: [rollouts/status]\n    resourceNames: [\"shop\"]\n    verbs: [patch]") {
		t.Fatalf("no status patch right:\n%s", out)
	}
	for _, field := range []string{"stableRS", "currentPodHash", "canary", "phase"} {
		if !strings.Contains(out, "object.status."+field+" == oldObject.status."+field) {
			t.Errorf("the policy does not hold status.%s", field)
		}
	}
	for _, field := range []string{"pauseConditions", "promoteFull", "abort =="} {
		if strings.Contains(out, "object.status."+field+" ==") {
			t.Errorf("the policy holds status.%s, which commands set", field)
		}
	}
	vf := filepath.Join(t.TempDir(), "values.yaml")
	os.WriteFile(vf, []byte(base+"    rbac: { controlRollouts: { other: [x] } }\n"), 0o644)
	if out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", vf).CombinedOutput(); err == nil ||
		!strings.Contains(string(out), "not in delivery.deployRunner.rolloutNamespaces") {
		t.Fatalf("a namespace outside rolloutNamespaces: %v %s", err, out)
	}
}

// Image steps in the chart: nothing in any configuration file by default (so
// the release before can read it); with builders set, the runners' file has
// an images section that loads, the TLS and signing Secrets are mounted, and
// a TCP builder without a TLS Secret is refused.
func TestChartImageSettings(t *testing.T) {
	for name, f := range renderChart(t, "postgresql: { enabled: true }\n") {
		if strings.Contains(f, "images:") {
			t.Errorf("the default %s configuration has images:\n%s", name, f)
		}
	}
	values := `postgresql: { enabled: true }
runner:
  secretStores:
    vault: { type: vault, address: https://vault.example:8200, role: launchway }
  images:
    builders:
      - { name: shared, address: tcp://buildkitd.launchway-build.svc:1234, serverName: buildkitd.launchway-build.svc, platforms: [linux/amd64], projects: [shop] }
    tlsSecret: buildkit-client
    registries:
      harbor:
        host: harbor.example.com
        credentials: { username: robot$ci, password: { store: vault, key: ci/harbor, field: password } }
        pullCredentials: { usernameFrom: { store: vault, key: ci/harbor-ro, field: user }, password: { store: vault, key: ci/harbor-ro, field: password } }
        pullProjects: [shop]
        push: [ { projects: [shop], repositories: ["homelab/**"] } ]
    signingSecret: launchway-cosign
    scanner: { dbRepository: harbor.example.com/mirror/trivy-db }
    maxImageMiB: 4096
`
	files := renderChart(t, values)
	c, err := LoadRunner(writeTemp(t, files["runner"]))
	if err != nil {
		t.Fatalf("runner: %v\n%s", err, files["runner"])
	}
	im := c.Images
	if im == nil || len(im.Builders) != 1 || im.Builders[0].CAFile != "/var/run/launchway/buildkit/ca.crt" ||
		im.Builders[0].ServerName != "buildkitd.launchway-build.svc" || im.Registries["harbor"].PullCredentials.UsernameFrom.Key != "ci/harbor-ro" ||
		!im.Registries["harbor"].MayPush("shop", "homelab/shop") || im.Registries["harbor"].MayPull("other") || im.Signing.KeyFile != "/var/run/launchway/signing/cosign.key" ||
		im.Scanner.DBRepository != "harbor.example.com/mirror/trivy-db" || im.MaxImageMiB != 4096 {
		t.Fatalf("images: %+v", im)
	}
	out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", writeTemp(t, values)).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"secretName: buildkit-client", "mountPath: /var/run/launchway/buildkit", "secretName: launchway-cosign"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("the runner Deployment has no %q", want)
		}
	}
	bad := strings.Replace(values, "    tlsSecret: buildkit-client\n", "", 1)
	if out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", writeTemp(t, bad)).CombinedOutput(); err == nil ||
		!strings.Contains(string(out), "needs mutual TLS") {
		t.Fatalf("a TCP builder without TLS: %v %s", err, out)
	}
}

// chartVerificationAI sets health sources, the model registry and the
// runners' ai switches, as values.yaml's examples do.
const chartVerificationAI = `postgresql: { enabled: true }
runner:
  ai: true
  secretStores:
    vault: { type: vault, address: https://vault.example:8200, role: launchway }
ai:
  models:
    - name: local
      provider: openai
      endpoint: http://ollama.ai.svc:11434/v1
      model: "qwen3:8b"
      projects: ["*"]
      schema_mode: json_object
    - name: claude
      provider: anthropic
      model: claude-sonnet-5-5
      key: { store: vault, key: ai/anthropic, field: key }
      projects: [shop]
      max_tokens: 8192
      tokens_per_day: 2000000
  serverSecretStores:
    vault: { type: vault, address: https://vault.example:8200, role: launchway-server }
delivery:
  environments:
    - { name: prod, target: homelab, projects: [shop] }
  verification:
    sources:
      - name: prometheus
        type: prometheus
        url: http://prometheus-operated.monitoring.svc:9090
        projects: ["*"]
      - name: loki
        type: loki
        url: http://loki-gateway.monitoring.svc
        projects: [shop]
        headers: { X-Scope-OrgID: homelab }
        limits: { maxLines: 2000 }
      - name: clickhouse
        type: clickhouse
        url: http://clickhouse.analytics.svc:8123
        projects: [shop]
        auth: { user: launchway, secret: { store: vault, key: ci/clickhouse, field: password } }
        timeout: 20s
        caFile: /etc/launchway-ca/ca.crt
        statements:
          checkout_errors:
            kind: metric
            sql: >-
              SELECT toStartOfMinute(ts) AS t, countIf(status >= 500) / count() AS value
              FROM requests WHERE service = {service:String}
              AND ts BETWEEN {start:DateTime64(3)} AND {end:DateTime64(3)} GROUP BY t ORDER BY t
            params: { service: String }
  deployRunner:
    enabled: true
    ai: true
    target: homelab
    applications: [ { projects: [shop], names: [shop] } ]
    rolloutNamespaces: [shop]
    secretStores:
      vault: { type: vault, address: https://vault.example:8200, role: launchway-deploy }
    repos:
      gitops:
        url: http://git.example/gitops.git
        paths: [ { projects: [shop], files: ["apps/shop/**"] } ]
`

// Health sources and models in the chart: nothing in any configuration file
// by default, nor with delivery set but these left out (so the release
// before can read it); with them set, the servers get the names and the
// registry, the deploy runner the sources' connections, and every file
// loads.
func TestChartVerificationAndAI(t *testing.T) {
	plain := `postgresql: { enabled: true }
delivery:
  environments:
    - { name: prod, target: homelab, projects: [shop] }
  deployRunner:
    enabled: true
    target: homelab
    applications: [ { projects: [shop], names: [shop] } ]
    rolloutNamespaces: [shop]
    repos:
      gitops:
        url: http://git.example/gitops.git
        paths: [ { projects: [shop], files: ["apps/shop/**"] } ]
`
	for _, values := range []string{"postgresql: { enabled: true }\n", plain} {
		for name, f := range renderChart(t, values) {
			for _, key := range []string{"verification", "health_sources", "ai:", "models"} {
				if strings.Contains(f, key) {
					t.Errorf("the default %s configuration has %s:\n%s", name, key, f)
				}
			}
		}
	}

	files := renderChart(t, chartVerificationAI)
	t.Setenv("LAUNCHWAY_DEPLOY_POOL_TOKEN", strings.Repeat("x", 48))
	c, err := Load(writeTemp(t, files["server"]))
	if err != nil {
		t.Fatalf("server: %v\n%s", err, files["server"])
	}
	hs := c.EngineHealthSources()
	if len(hs) != 3 || hs[0].Name != "prometheus" || hs[1].Type != "loki" || hs[2].Name != "clickhouse" || hs[2].Projects[0] != "shop" {
		t.Fatalf("verification.sources: %+v", hs)
	}
	reg, err := c.Models()
	if err != nil || reg == nil {
		t.Fatalf("models: %v", err)
	}
	if m, ok := reg.Get("claude"); !ok || m.Key == nil || m.Key.Key != "ai/anthropic" || m.MaxTokens != 8192 || m.TokensPerDay != 2000000 {
		t.Fatalf("claude: %+v", m)
	}
	if m, ok := reg.Get("local"); !ok || m.Endpoint != "http://ollama.ai.svc:11434/v1" || m.SchemaMode != "json_object" {
		t.Fatalf("local: %+v", m)
	}
	if _, ok := c.AI.Secrets["vault"]; !ok {
		t.Fatalf("ai.secrets: %+v", c.AI.Secrets)
	}
	if strings.Contains(files["server"], "url:") || strings.Contains(files["server"], "ci/clickhouse") {
		t.Fatalf("the servers' file has a source's connection:\n%s", files["server"])
	}

	d, err := LoadRunner(writeTemp(t, files["deploy-runner"]))
	if err != nil {
		t.Fatalf("deploy runner: %v\n%s", err, files["deploy-runner"])
	}
	if !d.AIEnabled() || len(d.HealthSources) != 3 {
		t.Fatalf("deploy runner: ai %v, %d health sources", d.AIEnabled(), len(d.HealthSources))
	}
	ch := d.HealthSources["clickhouse"]
	if ch.Type != "clickhouse" || ch.URL != "http://clickhouse.analytics.svc:8123" || ch.Auth == nil || ch.Auth.User != "launchway" ||
		ch.Auth.Secret.Store != "vault" || ch.Auth.Secret.Field != "password" || ch.Timeout != "20s" || ch.CAFile != "/etc/launchway-ca/ca.crt" ||
		ch.Statements["checkout_errors"].Params["service"] != "String" || !strings.Contains(ch.Statements["checkout_errors"].SQL, "{start:DateTime64(3)}") {
		t.Fatalf("clickhouse: %+v", ch)
	}
	if l := d.HealthSources["loki"]; l.Headers["X-Scope-OrgID"] != "homelab" || l.Limits.MaxLines != 2000 {
		t.Fatalf("loki: %+v", l)
	}
	ch.CAFile = "" // the file is mounted in the pod, not here
	d.HealthSources["clickhouse"] = ch
	lookup := func(context.Context, string, pipeline.SecretRef) (string, error) { return "", nil }
	if _, err := hsource.Open(d.HealthSources, lookup); err != nil {
		t.Fatalf("the deploy runner's health sources do not open: %v", err)
	}

	r, err := LoadRunner(writeTemp(t, files["runner"]))
	if err != nil {
		t.Fatalf("runner: %v\n%s", err, files["runner"])
	}
	if !r.AIEnabled() || len(r.HealthSources) != 0 {
		t.Fatalf("runner: ai %v, health sources %v", r.AIEnabled(), r.HealthSources)
	}
}

// The chart refuses health source and model values that cannot work.
func TestChartVerificationAIRefused(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	for _, tc := range []struct{ name, from, to, want string }{
		{"sources without a deploy runner", "    enabled: true\n    ai: true\n", "    enabled: false\n",
			"health sources are read by the deploy runner; enable delivery.deployRunner"},
		{"a source named twice", "      - name: loki\n", "      - name: prometheus\n", "prometheus is listed twice"},
		{"a source without projects", "        projects: [\"*\"]\n", "        projects: []\n", "/delivery/verification/sources/0/projects"},
		{"a source secret in an unknown store", "secret: { store: vault, key: ci/clickhouse", "secret: { store: other, key: ci/clickhouse",
			`auth.secret.store "other" is not in delivery.deployRunner.secretStores`},
		{"an unknown source type", "        type: loki\n", "        type: mysql\n", "/delivery/verification/sources/1/type"},
		{"a model with an unknown provider", "      provider: openai\n", "      provider: other\n", "/ai/models/0/provider"},
		{"an openai model without an endpoint", "      endpoint: http://ollama.ai.svc:11434/v1\n", "", "endpoint is required for provider openai"},
		{"a hosted model without a key", "      key: { store: vault, key: ai/anthropic, field: key }\n", "", "key is required for the hosted API"},
		{"a model named twice", "    - name: claude\n", "    - name: local\n", "local is listed twice"},
		{"verification in extraConfig", "postgresql: { enabled: true }\n", "postgresql: { enabled: true }\nconfig: { extraConfig: { verification: {} } }\n",
			"config.extraConfig.verification is not allowed"},
	} {
		values := strings.Replace(chartVerificationAI, tc.from, tc.to, 1)
		if values == chartVerificationAI {
			t.Fatalf("%s: the values did not change", tc.name)
		}
		out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", writeTemp(t, values)).CombinedOutput()
		if err == nil || !strings.Contains(string(out), tc.want) {
			t.Errorf("%s: %v %s", tc.name, err, out)
		}
	}
	noModels := `postgresql: { enabled: true }
runner: { ai: true }
`
	if out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f", writeTemp(t, noModels)).CombinedOutput(); err == nil ||
		!strings.Contains(string(out), "runner.ai is true, but ai.models is empty") {
		t.Errorf("runner.ai without models: %v %s", err, out)
	}
}
