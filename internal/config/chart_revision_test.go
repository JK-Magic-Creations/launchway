package config

import (
	"os"
	"strings"
	"testing"
)

func TestChartNotice(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if m, w := ChartNotice(env(""), false); m != "" || w {
		t.Fatal(m)
	}
	m, w := ChartNotice(env(""), true)
	if !w || !strings.Contains(m, "rendered by an older chart") || !strings.Contains(m, "encryption and network settings from the new chart are not applied yet") {
		t.Fatal(m)
	}
	if m, w := ChartNotice(env("1"), true); !w || !strings.Contains(m, "revision 1 but expects") {
		t.Fatal(m)
	}
	if m, w := ChartNotice(env("2"), true); w || !strings.Contains(m, "as this server expects") {
		t.Fatal(m, w)
	}
	if _, w := ChartNotice(env("99"), true); w {
		t.Fatal("a newer chart is not a warning")
	}
}

// The binary and the chart name the same revision: whoever raises one raises the other.
func TestChartRevisionMatchesTheChart(t *testing.T) {
	b, err := os.ReadFile("../../deploy/helm/launchway/Chart.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := `launchway.io/chart-revision: "` + string(rune('0'+ChartRevision)) + `"`
	if !strings.Contains(string(b), want) {
		t.Fatalf("Chart.yaml does not carry %s", want)
	}
	if !strings.Contains(string(mustRead(t, "../../deploy/helm/launchway/templates/_encryption.tpl")), EnvChartRevision) {
		t.Fatal("the chart does not set " + EnvChartRevision)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIgnoredValuesNotice(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if IgnoredValuesNotice(env("")) != "" {
		t.Fatal("none ignored must say nothing")
	}
	m := IgnoredValuesNotice(env("networkPolicy.x,typo,a.b"))
	if !strings.Contains(m, "3 values were ignored by this chart version: networkPolicy.x, typo, a.b: a typo, or a setting for a newer chart") {
		t.Fatal(m)
	}
}
