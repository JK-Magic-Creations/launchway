package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jk-magic-creations/launchway/internal/engine"
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

// Policies in the chart: the default render writes no new key (so the
// version before can read it), and set values render to a file that loads,
// with the policies checked.
func TestChartPolicySettings(t *testing.T) {
	def := renderChart(t, "postgresql: { enabled: true }\n")
	for name, f := range def {
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(f), &doc); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"policy", "policies"} {
			if _, ok := doc[key]; ok {
				t.Errorf("the default %s configuration has %s:\n%s", name, key, f)
			}
		}
	}
	files := renderChart(t, `postgresql: { enabled: true }
config:
  extraConfig:
    libraries: [ { name: org-policies, repo: "https://git.example.com/org/policies.git" } ]
  policy:
    waivers: { allow: operator, maxDays: 14 }
    sources:
      - { name: org, library: org-policies, version: v3, staleAfter: 2h }
    policies:
      - name: prod-evidence
        mode: enforce
        scope: { environments: [prod] }
        rules: { tests: { min: 1 } }
`)
	p := writeTemp(t, files["server"])
	c, err := Load(p)
	if err != nil {
		t.Fatalf("%v\n%s", err, files["server"])
	}
	o, err := c.PolicyOptions()
	if err != nil || len(o.Inline) != 1 || len(o.Sources) != 1 || o.Sources[0].StaleAfter.String() != "2h0m0s" || o.MaxWaiverDays != 14 {
		t.Fatalf("%+v %v", o, err)
	}
	if r, _ := c.WaiverRole(); r.String() != "operator" {
		t.Fatal(r)
	}
}

// The database store (schema 17). The chart's default render writes none
// of its settings, so a server of the release before reads the file: with
// nothing set, the store is chosen by the engine. Set, they load.
func TestChartDatabaseStoreSettings(t *testing.T) {
	for _, values := range []string{"postgresql: { enabled: true }\n", "database: { url: \"postgres://u@db/launchway\" }\n"} {
		def := renderChart(t, values)
		for _, key := range []string{"database:", "max_total_gib", "max_blob_mib", "store:", "artifacts:"} {
			if strings.Contains(def["server"], "  "+key) || strings.Contains(def["server"], "\n"+key) {
				t.Errorf("the default server configuration (%s) has %s:\n%s", strings.TrimSpace(values), key, def["server"])
			}
		}
	}
	srv := renderChart(t, `postgresql: { enabled: true }
config:
  artifacts:
    store: database
    database: { maxTotalGiB: 20, maxBlobMiB: 1024 }
`)["server"]
	c, err := Load(writeTemp(t, srv))
	if err != nil {
		t.Fatalf("server: %v\n%s", err, srv)
	}
	o, err := c.ArtifactLimits()
	if err != nil || o.Choice != engine.StoreDatabase || o.DatabaseLimits.MaxBytes != 20<<30 || o.DatabaseLimits.MaxObjectBytes != 1<<30 {
		t.Fatalf("rendered: %+v %v\n%s", o, err, srv)
	}
	// The limits alone, store unset: allowed (they apply when the database is chosen).
	srv = renderChart(t, "postgresql: { enabled: true }\nconfig: { artifacts: { database: { maxTotalGiB: 5 } } }\n")["server"]
	if c, err = Load(writeTemp(t, srv)); err != nil {
		t.Fatalf("server: %v\n%s", err, srv)
	}
	if o, err := c.ArtifactLimits(); err != nil || o.Choice != engine.StoreAuto || o.DatabaseLimits.MaxBytes != 5<<30 {
		t.Fatalf("limits alone: %+v %v", o, err)
	}
}

// Test attachments, coverage and HTML reports in the chart: nothing in the
// server's file by default (so the release before can read it); with the
// values set, a reports section that loads.
func TestChartReportSettings(t *testing.T) {
	def := renderChart(t, "postgresql: { enabled: true }\n")
	var top map[string]any
	if err := yaml.Unmarshal([]byte(def["server"]), &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["reports"]; ok {
		t.Fatalf("the default server configuration has reports:\n%s", def["server"])
	}
	files := renderChart(t, `postgresql: { enabled: true }
config:
  reports:
    maxAttachmentFiles: 500
    maxHtmlMiB: 64
    maxCoverageFilesPerRun: 1000
    origin: https://reports.ci.example.com
`)
	c, err := Load(writeTemp(t, files["server"]))
	if err != nil {
		t.Fatalf("server: %v\n%s", err, files["server"])
	}
	o, err := c.ReportOptions()
	if err != nil || o.AttachmentFiles != 500 || o.HTMLBytes != 64<<20 || o.CoverageFilesPerRun != 1000 || o.HTMLFiles != 0 {
		t.Fatalf("options %+v %v", o, err)
	}
	if origin, err := c.ReportsOrigin(); err != nil || origin != "https://reports.ci.example.com" {
		t.Fatalf("origin %q %v", origin, err)
	}
	if out, err := exec.Command("helm", "template", "t", "../../deploy/helm/launchway", "-f",
		writeTemp(t, "postgresql: { enabled: true }\nconfig: { extraConfig: { reports: { origin: x } } }\n")).CombinedOutput(); err == nil ||
		!strings.Contains(string(out), "use config.reports") {
		t.Fatalf("reports in extraConfig: %v %s", err, out)
	}
}

func TestReportsOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"":                                "",
		"https://reports.example.com":     "https://reports.example.com",
		"https://reports.example.com/":    "https://reports.example.com",
		"http://reports.example.com:8080": "http://reports.example.com:8080",
	} {
		c := &Config{Reports: ServerReportsConfig{Origin: in}}
		if got, err := c.ReportsOrigin(); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"reports.example.com", "ftp://x", "https://x/path", "https://user@x", "https://x?q=1"} {
		c := &Config{Reports: ServerReportsConfig{Origin: bad}}
		if _, err := c.ReportsOrigin(); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	c := &Config{Reports: ServerReportsConfig{MaxAttachmentFiles: -1}}
	if _, err := c.ReportOptions(); err == nil {
		t.Error("a negative limit was accepted")
	}
}

// The MCP endpoint in the chart: nothing in any configuration file by
// default (so the release before can read it, and the endpoint is on), and
// with values set, a file the server loads with them.
func TestChartMCPSettings(t *testing.T) {
	def := renderChart(t, "postgresql: { enabled: true }\n")
	for name, f := range def {
		if strings.Contains(f, "mcp") {
			t.Errorf("the default %s configuration has mcp:\n%s", name, f)
		}
	}
	if c, err := Load(writeTemp(t, def["server"])); err != nil || !c.MCP.On() {
		t.Fatalf("the endpoint is not on by default: %v", err)
	}
	files := renderChart(t, `postgresql: { enabled: true }
config:
  mcp:
    enabled: false
    allowedOrigins: [https://tools.example.com]
    requestsPerMinute: 60
    concurrent: 2
    total: 16
`)
	c, err := Load(writeTemp(t, files["server"]))
	if err != nil {
		t.Fatalf("server: %v\n%s", err, files["server"])
	}
	if c.MCP.On() || c.MCP.RequestsPerMinute != 60 || c.MCP.Concurrent != 2 || c.MCP.Total != 16 || len(c.MCP.AllowedOrigins) != 1 {
		t.Fatalf("mcp: %+v", c.MCP)
	}
}

// The default render, with either way of reaching PostgreSQL, writes none
// of the keys that policy gates (16), the database store (17), reports
// (18) and the MCP endpoint brought, in any configuration file, so a
// server of the release before them reads it.
func TestChartDefaultWritesNoNewKeys(t *testing.T) {
	for _, values := range []string{"postgresql: { enabled: true }\n", "database: { url: \"postgres://u@db/launchway\" }\n"} {
		for name, f := range renderChart(t, values) {
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(f), &doc); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"policy", "policies", "reports", "mcp"} {
				if _, ok := doc[key]; ok {
					t.Errorf("the default %s configuration (%s) has %s:\n%s", name, strings.TrimSpace(values), key, f)
				}
			}
			if a, ok := doc["artifacts"].(map[string]any); ok {
				for _, key := range []string{"store", "database"} {
					if _, ok := a[key]; ok {
						t.Errorf("the default %s configuration (%s) has artifacts.%s:\n%s", name, strings.TrimSpace(values), key, f)
					}
				}
			}
		}
	}
}
