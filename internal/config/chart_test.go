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
