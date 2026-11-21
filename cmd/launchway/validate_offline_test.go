package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateExpandsTemplatesOffline(t *testing.T) {
	lib := t.TempDir()
	os.MkdirAll(filepath.Join(lib, "templates"), 0o755)
	os.WriteFile(filepath.Join(lib, "templates", "check.yaml"), []byte("template: check\nparams:\n  who: { type: string, default: x }\nsteps:\n  a: { image: alpine, run: echo }\n"), 0o644)
	file := filepath.Join(t.TempDir(), "pipeline.yaml")
	os.WriteFile(file, []byte("pipeline: p\nlibraries:\n  platform: { repo: lib, version: v1 }\nsteps:\n  ci: { uses: platform/check }\n"), 0o644)
	if err := validateOffline(file, []string{"platform=" + lib}, false); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if err := validateOffline(file, nil, false); err == nil {
		t.Fatal("a library without a directory was accepted")
	}
	if err := validateOffline(file, []string{"platform"}, false); err == nil {
		t.Fatal("a bad --library was accepted")
	}
}
