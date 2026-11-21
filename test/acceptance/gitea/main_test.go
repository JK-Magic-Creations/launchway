package giteaaccept

import (
	"fmt"
	"os"
	"testing"
)

var cfg *Config

func TestMain(m *testing.M) {
	cfg = loadConfig()
	if cfg != nil {
		if cfg.Token == "" {
			fmt.Fprintln(os.Stderr, "LW_ACCEPT_URL is set but LW_ACCEPT_TOKEN is not")
			os.Exit(2)
		}
		if err := cfg.whoami(); err != nil {
			fmt.Fprintln(os.Stderr, cfg.scrub(err.Error()))
			os.Exit(2)
		}
	}
	code := m.Run()
	if dir := os.Getenv("LW_ACCEPT_REPORT"); dir != "" && cfg != nil {
		if err := writeReport(dir, cfg.URL, cfg.Flavor); err != nil {
			fmt.Fprintln(os.Stderr, "report:", err)
		}
	}
	os.Exit(code)
}

// host skips the test unless a host is configured, and returns the configuration.
func host(t *testing.T) *Config {
	t.Helper()
	if cfg == nil {
		t.Skip("LW_ACCEPT_URL is not set: the acceptance suite needs a host to test")
	}
	return cfg
}

// TestCatalog is the list of acceptance tests: 31 plus the census.
func TestCatalog(t *testing.T) {
	if len(Catalog) != 32 {
		t.Fatalf("catalog has %d entries, want 31 acceptance tests plus AT-GAP-9", len(Catalog))
	}
	seen := map[string]bool{}
	for _, e := range Catalog {
		if seen[e.ID] || (e.Coverage != Full && e.Why == "") {
			t.Errorf("%s: duplicate, or not automated without a reason", e.ID)
		}
		seen[e.ID] = true
	}
}
