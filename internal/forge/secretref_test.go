package forge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretRefResolveReadsButNeverShowsTheValue(t *testing.T) {
	t.Setenv("LW_TEST_TOKEN", " s3cr3t \n")
	if v, err := (SecretRef{Env: "LW_TEST_TOKEN"}).Resolve(); err != nil || v != "s3cr3t" {
		t.Fatalf("%q %v", v, err)
	}
	f := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(f, []byte("filetoken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := (SecretRef{File: f}).Resolve(); err != nil || v != "filetoken" {
		t.Fatalf("%q %v", v, err)
	}
	for _, bad := range []SecretRef{{}, {Env: "LW_NOT_SET"}, {File: filepath.Join(t.TempDir(), "missing")}, {Env: "ghp_literal"}, {Env: "A", File: f}} {
		if v, err := bad.Resolve(); err == nil || v != "" {
			t.Errorf("%+v resolved to %q", bad, v)
		}
	}
	if got := (SecretRef{Env: "X"}).String(); got != "env:X" {
		t.Error(got)
	}
	if _, err := (SecretRef{Env: "LW_TEST_TOKEN"}).Resolve(); err != nil && strings.Contains(err.Error(), "s3cr3t") {
		t.Error("error repeats the value")
	}
}
