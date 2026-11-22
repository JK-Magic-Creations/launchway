package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func helmRender(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("needs helm")
	}
	a := append([]string{"template", "t", "../../deploy/helm/launchway", "--set", "externalURL=https://launchway.example.com"}, args...)
	out, err := exec.Command("helm", a...).CombinedOutput()
	return string(out), err
}

// A values file written for a newer chart renders under this one: the unknown keys are listed, not fatal.
func TestChartIgnoresUnknownValuesAndSaysSo(t *testing.T) {
	out, err := helmRender(t, "--set", "typoTop=1", "--set", "networkPolicy.fromTheFuture.x=2", "--set", "config.nope=3")
	if err != nil {
		t.Fatalf("unknown values must not stop the render: %v\n%s", err, out)
	}
	want := "config.nope,networkPolicy.fromTheFuture,typoTop"
	for _, w := range []string{"LAUNCHWAY_IGNORED_VALUES", `value: "` + want + `"`, `launchway.io/ignored-values: "` + want + `"`} {
		if !strings.Contains(out, w) {
			t.Fatalf("render lacks %q", w)
		}
	}
}

func TestChartStrictValuesRefusesUnknownAndSaysWhich(t *testing.T) {
	out, err := helmRender(t, "--set", "typoTop=1", "--set", "strictValues=true")
	if err == nil || !strings.Contains(out, "does not know these values: typoTop") {
		t.Fatalf("strictValues must refuse a typo and name it: %v\n%s", err, out)
	}
}

// Known keys keep their types even though the schema no longer refuses unknown ones.
func TestChartKnownValuesStayTyped(t *testing.T) {
	if out, err := helmRender(t, "--set", "server.replicas=many"); err == nil {
		t.Fatalf("a known key of the wrong type must fail: %s", out)
	}
}

// The chart's own defaults and examples name no key the chart does not know (so a typo in the
// known-values list cannot hide behind the warning).
func TestChartDefaultsAndExamplesIgnoreNothing(t *testing.T) {
	out, err := helmRender(t)
	if err != nil || strings.Contains(out, "LAUNCHWAY_IGNORED_VALUES") {
		t.Fatalf("defaults: %v\n%s", err, out)
	}
	ex, _ := filepath.Glob("../../deploy/helm/launchway/examples/*.yaml")
	for _, f := range ex {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		out, err := helmRender(t, "-f", f, "--set", "strictValues=true")
		if err != nil {
			t.Errorf("%s: %v\n%s", f, err, out)
		}
	}
}

// The image and the chart can arrive separately. An image from before the secrets bootstrap gets no
// hook Job, no key mount and no encryption environment (it has no such command and no shell to
// probe with); the current one gets them; a tag outside the supported range fails with a sentence.
func TestChartToleratesAnOlderImage(t *testing.T) {
	old, err := helmRender(t, "--set", "image.tag=0.1.0")
	if err != nil {
		t.Fatalf("the release before must render: %v\n%s", err, old)
	}
	for _, bad := range []string{"bootstrap-secrets", "root-key", "LAUNCHWAY_ENCRYPTION_REQUIRE", "LAUNCHWAY_ROOT_KEY_FILE", "launchway-keys"} {
		if strings.Contains(old, bad) {
			t.Errorf("an image older than the bootstrap must not get %q", bad)
		}
	}
	cur, err := helmRender(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bootstrap-secrets", "LAUNCHWAY_ENCRYPTION_REQUIRE", "launchway-keys"} {
		if !strings.Contains(cur, want) {
			t.Errorf("the current image must get %q", want)
		}
	}
	if out, err := helmRender(t, "--set", "image.tag=0.0.5"); err == nil || !strings.Contains(out, "supports Launchway images >=0.1.0 <0.3.0, and image.tag is 0.0.5") {
		t.Fatalf("an unsupported tag must fail with a sentence: %v\n%s", err, out)
	}
	if out, err := helmRender(t, "--set", "image.tag=sha-1234", "--set", "image.bootstrap=false"); err != nil || strings.Contains(out, "bootstrap-secrets") {
		t.Fatalf("image.bootstrap=false must skip the hook: %v", err)
	}
}

// The server's ingress is never "any source": behind an Ingress it admits its own namespace, the
// Gateway's and proxyNamespaces, and nothing else.
func TestChartServerIngressIsNeverAnySource(t *testing.T) {
	out, err := helmRender(t, "--set", "ingress.enabled=true", "--set", "ingress.hosts[0]=launchway.example.com", "--set", "networkPolicy.server.proxyNamespaces={ingress-nginx}")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range chartDocs(t, out) {
		if d["kind"] != "NetworkPolicy" || !strings.HasSuffix(name(d), "-server") {
			continue
		}
		for _, rule := range dig(d, "spec", "ingress").([]any) {
			if _, ok := dig(rule, "from").([]any); !ok {
				if dig(rule, "ports") != nil && !strings.Contains(out, "ssh") { // a rule with ports and no peers admits every source
					t.Errorf("an ingress rule has no source: %v", rule)
				}
			}
		}
		if !strings.Contains(out, "kubernetes.io/metadata.name: ingress-nginx") {
			t.Error("the proxy's namespace is not admitted")
		}
		return
	}
	t.Fatal("no server policy")
}

// A fresh install with the defaults must start: the artifact limits the chart derives for the 1Gi
// default volume have to be ones the server accepts (found by the upgrade matrix's install cell: the
// largest single file, 2 GiB by default, was above the 1 GiB cap and the servers refused to start).
func TestChartDefaultArtifactLimitsAreAccepted(t *testing.T) {
	for _, values := range []string{"", "postgresql: { persistence: { size: 8Gi } }\n", "postgresql: { persistence: { size: 3Gi } }\n"} {
		srv := renderChart(t, values)["server"]
		c, err := Load(writeTemp(t, srv))
		if err != nil {
			t.Fatalf("%q: %v\n%s", values, err, srv)
		}
		if _, err := c.ArtifactLimits(); err != nil {
			t.Errorf("%q: the derived artifact limits are refused by the server: %v\n%s", values, err, srv)
		}
	}
}
