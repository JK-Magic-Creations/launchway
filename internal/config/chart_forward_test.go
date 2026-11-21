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
