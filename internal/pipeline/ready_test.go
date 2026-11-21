package pipeline

import (
	"strings"
	"testing"
)

func readySrc(ready string) string {
	return "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true, " + ready + " }\nsteps:\n  t: { image: x, run: y }\n"
}

func TestServiceReady(t *testing.T) {
	p, err := Parse([]byte(readySrc(`ready: { run: "pg_isready -U postgres", every: 3s, within: 90s }`)))
	if err != nil {
		t.Fatal(err)
	}
	r := p.Container("db").Ready
	if r == nil || r.Kind() != "run" || r.Every.Short() != "3s" || r.Within.Short() != "1m30s" || p.Requires != SchemaParity2 {
		t.Fatalf("%+v requires %d", r, p.Requires)
	}
	r = mustParse(t, readySrc(`ready: { tcp: 6379 }`)).Container("db").Ready
	if r.Kind() != "tcp" || r.Every.D() != DefaultReadyEvery || r.Within.D() != DefaultReadyWithin {
		t.Fatalf("%+v", r)
	}
	r = mustParse(t, readySrc(`ready: { http: { path: /health, port: 8080 } }`)).Container("db").Ready
	if r.Kind() != "http" || r.HTTP.Port != 8080 || !strings.Contains(r.Describe(), "localhost:8080/health") {
		t.Fatalf("%+v", r)
	}
	if mustParse(t, "pipeline: p\ncontainers:\n  db: { image: postgres:17, service: true }\nsteps:\n  t: { image: x, run: y }\n").Requires != 0 {
		t.Fatal("a service without ready is unchanged")
	}
}

func mustParse(t *testing.T, src string) *Pipeline {
	t.Helper()
	p, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestServiceReadyMistakes(t *testing.T) {
	for in, want := range map[string]string{
		"ready: { run: x, tcp: 1 }":                   "exactly one of run, tcp or http",
		"ready: {}":                                   "exactly one",
		"ready: { tcp: 70000 }":                       "1 to 65535",
		"ready: { http: { path: health, port: 80 } }": "starts with /",
		"ready: { http: { path: /h } }":               "which port",
		"ready: { run: x, every: 100ms }":             "at least 1s",
		"ready: { run: x, within: 11m }":              "between 1s and 10m",
		"ready: { run: x, every: 30s, within: 10s }":  "longer than within",
		"ready: { run: x, every: soon }":              "duration",
		"ready: 6379":                                 "ready is a mapping",
		"ready: { postgres: yes }":                    "postgres",
	} {
		if _, err := Parse([]byte(readySrc(in))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", in, want, err)
		}
	}
	_, err := Parse([]byte("pipeline: p\ncontainers:\n  go: { image: golang, ready: { tcp: 1 } }\nsteps:\n  t: { container: go, run: y }\n"))
	if err == nil || !strings.Contains(err.Error(), "only a service is waited for") {
		t.Errorf("a container that is not a service: %v", err)
	}
}
