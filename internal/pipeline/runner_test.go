package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunnerKey(t *testing.T) {
	p, err := Parse([]byte("pipeline: p\nrunner: {pool: gpu, labels: [arm64, cuda]}\nsteps:\n  a: {image: x, run: a}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Runner == nil || p.Runner.Pool != "gpu" || strings.Join(p.Runner.Labels, ",") != "arm64,cuda" || p.Requires != SchemaPools {
		t.Fatalf("%+v %d", p.Runner, p.Requires)
	}
	// It survives the run's JSON, which servers read back.
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"runner":{"pool":"gpu"`) || !strings.Contains(string(b), `"requires":22`) {
		t.Fatalf("%s", b)
	}
	for src, want := range map[string]string{
		"runner: {}":                      "name a pool, labels or both",
		"runner: {pool: GPU}":             "not a pool name",
		"runner: {labels: [a, a]}":        "listed twice",
		"runner: {labels: [a], extra: 1}": "extra",
		"runner: gpu":                     "runner",
	} {
		_, err := Parse([]byte("pipeline: p\n" + src + "\nsteps:\n  a: {image: x, run: a}\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", src, err)
		}
	}
	if _, err := Parse([]byte("pipeline: p\nruns_on: x\nsteps:\n  a: {image: x, run: a}\n")); err == nil || !strings.Contains(err.Error(), "runner:") {
		t.Errorf("runs_on: %v", err)
	}
}
