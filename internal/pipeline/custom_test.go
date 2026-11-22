package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testStepYAML = `step: notes
contract: 1
description: Write release notes
toolchain: python@3.13
command: python3 main.py
inputs:
  title: { type: string, description: The title, pattern: "^[A-Za-z ]+$", max: 20 }
  count: { type: number, default: 3, min: 1, max: 10 }
  dry_run: { type: boolean, default: false }
  files: { type: list, default: [a, b] }
  extra: { type: record, required: false }
  token: { type: secret }
outputs:
  url: { type: string }
  total: { type: number }
permissions:
  network: [registry.npmjs.org]
  write: [dist]
`

func stepRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "steps", "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "steps", "notes", "step.yaml"), []byte(testStepYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func expandCustom(t *testing.T, with string) (*Pipeline, error) {
	t.Helper()
	lib, self := stepRepo(t), stepRepo(t)
	text := "pipeline: p\nlibraries:\n  platform: { repo: tools, version: v1 }\nsteps:\n  notes:\n    uses: platform/notes\n    with: { " + with + " }\n"
	return ExpandOffline([]byte(text), map[string]string{"platform": lib, SelfLibrary: self}, BindOptions{}, nil)
}

func TestCustomStepExpandsIntoOneStepThatCarriesItsPermissions(t *testing.T) {
	p, err := expandCustom(t, `title: Hello, token_store: vault, token_key: apps/x/token`)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 1 {
		t.Fatalf("steps: %d", len(p.Steps))
	}
	s := p.Steps[0]
	if s.ID != "notes/run" || s.Custom == nil || s.Custom.Name != "notes" || s.Custom.Library != "platform" || s.Custom.Commit != OfflineCommit {
		t.Fatalf("custom: %+v", s.Custom)
	}
	if strings.Join(s.Custom.Network, ",") != "registry.npmjs.org" || strings.Join(s.Custom.Write, ",") != "dist" || strings.Join(s.Custom.Secrets, ",") != "token" {
		t.Errorf("permissions: %+v", s.Custom)
	}
	if s.Custom.OutputTypes["total"] != "number" || s.Custom.Values["count"] != "3" || s.Custom.Values["files"] != `["a","b"]` {
		t.Errorf("values: %+v", s.Custom)
	}
	if ref := s.Secrets["TOKEN"]; ref.Store != "vault" || ref.Key != "apps/x/token" {
		t.Errorf("secret: %+v", s.Secrets)
	}
	if s.Env["PARAM_COUNT"] != "3" {
		t.Errorf("env: %v", s.Env)
	}
	if p.Requires < SchemaCustomSteps {
		t.Errorf("requires %d", p.Requires)
	}
	if s.Reports == nil || len(s.Reports.Findings) == 0 || len(s.Artifacts) != 1 {
		t.Errorf("reports/artifacts: %+v %+v", s.Reports, s.Artifacts)
	}
}

func TestCustomStepWithIsCheckedBeforeARun(t *testing.T) {
	for _, c := range []struct{ with, want string }{
		{`title: Hello, bogus: 1, token_store: v, token_key: k`, `has no input "bogus"`},
		{`token_store: v, token_key: k`, "needs a value for title"},
		{`title: Hello, count: many, token_store: v, token_key: k`, "count is a number"},
		{`title: Hello, count: 11, token_store: v, token_key: k`, "count is at most 10"},
		{`title: "Hello 123", token_store: v, token_key: k`, "must match"},
		{`title: Hello, extra: "[1]", token_store: v, token_key: k`, "extra is a record"},
		{`title: Hello, token_store: v`, "token_key"},
	} {
		_, err := expandCustom(t, c.with)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("with %s: got %v, want %q", c.with, err, c.want)
		}
	}
}

func TestStepOfTheSameRepository(t *testing.T) {
	lib, self := stepRepo(t), stepRepo(t)
	_ = lib
	text := "pipeline: p\nsteps:\n  notes:\n    uses: ./steps/notes\n    with: { title: Hi, token_store: v, token_key: k }\n"
	p, err := ExpandOffline([]byte(text), map[string]string{SelfLibrary: self}, BindOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := p.Steps[0]
	if s.Custom == nil || s.Custom.Library != SelfLibrary || !strings.Contains(s.Run, `"$PWD"/steps/notes`) {
		t.Errorf("%+v %q", s.Custom, s.Run)
	}
	if _, err := ExpandOffline([]byte(text), nil, BindOptions{}, nil); err == nil || !strings.Contains(err.Error(), "files are not known") {
		t.Errorf("without the repository: %v", err)
	}
}
