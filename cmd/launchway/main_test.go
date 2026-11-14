package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// A server without sign-in refuses to listen where others can reach it,
// before it opens anything, unless told that is intended.
func TestServerRefusesAnonymousExposure(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(cfg, []byte("data: data\nrunner: {enabled: false}\n"), 0o644)
	t.Setenv("LAUNCHWAY_DATABASE", "")

	err := cmdServer([]string{"--config", cfg, "--listen", "0.0.0.0:0"})
	if err == nil || !strings.Contains(err.Error(), "refusing to listen on 0.0.0.0:0 without sign-in") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data")); !os.IsNotExist(err) {
		t.Fatalf("the server created its data directory before refusing: %v", err)
	}

	os.WriteFile(cfg, []byte("listen: 0.0.0.0:0\ndata: data\nrunner: {enabled: false}\n"), 0o644)
	if err := cmdServer([]string{"--config", cfg}); err == nil || !strings.Contains(err.Error(), "without sign-in") {
		t.Fatalf("listen from the file: %v", err)
	}
}

// A shared runner must not be able to read its own environment for
// pipelines; only a local run may.
func TestOnlyLocalRunsGetTheEnvStore(t *testing.T) {
	t.Setenv("LW_MAIN_TEST", "value")
	ref := pipeline.SecretRef{Store: "env", Key: "LW_MAIN_TEST"}
	shared, err := openSecrets("")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := shared.Lookup(context.Background(), "p", ref); err == nil {
		t.Fatalf("a shared runner read its environment: %q", v)
	}
	local, err := localSecrets("")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := local.Lookup(context.Background(), "p", ref); err != nil || v != "value" {
		t.Fatalf("local run: %q, %v", v, err)
	}
}
