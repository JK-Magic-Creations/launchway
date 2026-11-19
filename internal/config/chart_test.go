package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		!im.Registries["harbor"].MayPush("shop", "homelab/shop") || im.Signing.KeyFile != "/var/run/launchway/signing/cosign.key" ||
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
