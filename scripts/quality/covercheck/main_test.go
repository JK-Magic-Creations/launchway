package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundDownNeverRoundsUp(t *testing.T) {
	for in, want := range map[float64]float64{82.96: 82.9, 100: 100, 0.04: 0, 57.1: 57.1} {
		if got := roundDown(in); got != want {
			t.Errorf("roundDown(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestParseReadsGoTestOutput(t *testing.T) {
	out := "ok  \tgithub.com/jk-magic-creations/launchway/internal/redact\t0.01s\tcoverage: 100.0% of statements\n" +
		"?   \tgithub.com/jk-magic-creations/launchway/internal/ids\t[no test files]\n" +
		"ok  \tgithub.com/jk-magic-creations/launchway/internal/auth\t0.2s\tcoverage: 52.9% of statements\n"
	path := filepath.Join(t.TempDir(), "cov.txt")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	got := parse(path)
	want := map[string]float64{"internal/redact": 100, "internal/ids": 0, "internal/auth": 52.9}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
}
