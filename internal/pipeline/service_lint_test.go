package pipeline

import (
	"strings"
	"testing"
)

func TestServicesUsedByManyStepsAreWarnedAbout(t *testing.T) {
	const two = "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true }\n  go: { image: golang:1.27 }\nsteps:\n  migrate: { container: go, run: m }\n  test: { container: go, run: t, needs: migrate }\n"
	p, err := Parse([]byte(two))
	if err != nil {
		t.Fatal(err)
	}
	w := ServiceWarnings(p)
	if len(w) != 1 || w[0].Code != "service-per-step" || w[0].Path != "containers.db" ||
		!strings.Contains(w[0].Message, "migrate, test") || !strings.Contains(w[0].Message, "started again with each step") || !strings.Contains(w[0].Message, "kubernetes.pod: per-run") {
		t.Fatalf("%+v", w)
	}
	for name, text := range map[string]string{
		"one step":    "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true }\nsteps:\n  t: { image: x, run: t }\n",
		"no services": "pipeline: p\nsteps:\n  a: { image: x, run: t }\n  b: { image: x, run: t }\n",
	} {
		p, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if w := ServiceWarnings(p); len(w) != 0 {
			t.Errorf("%s: %+v", name, w)
		}
	}
}
