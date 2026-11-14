package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/auth"
)

// store is a SharedStore in memory.
type store map[string]string

func (s store) SharedValue(key, proposed string) (string, error) {
	if v, ok := s[key]; ok {
		return v, nil
	}
	s[key] = proposed
	return proposed, nil
}

func TestRunnerToken(t *testing.T) {
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "")
	shared := store{}

	// An existing installation's token is offered to the database, so its
	// runners keep working.
	a := t.TempDir()
	os.WriteFile(TokenFile(a), []byte("old-token\n"), 0o600)
	tok, err := RunnerToken(a, shared)
	if err != nil || tok != "old-token" {
		t.Fatalf("token %q, %v", tok, err)
	}

	// A second server with no token, or a different one, adopts the shared
	// token and mirrors it to its data directory.
	b := t.TempDir()
	if tok, _ := RunnerToken(b, shared); tok != "old-token" {
		t.Fatalf("second server got %q", tok)
	}
	if data, _ := os.ReadFile(TokenFile(b)); strings.TrimSpace(string(data)) != "old-token" {
		t.Fatalf("token file %q", data)
	}

	// A fresh database and data directory get a new random token.
	if tok, _ := RunnerToken(t.TempDir(), store{}); len(tok) != 48 {
		t.Fatalf("new token %q", tok)
	}

	// The environment wins.
	t.Setenv("LAUNCHWAY_RUNNER_TOKEN", "from-env")
	if tok, _ := RunnerToken(a, shared); tok != "from-env" {
		t.Fatalf("token %q", tok)
	}
}

func TestDatabaseAddr(t *testing.T) {
	t.Setenv("LAUNCHWAY_DATABASE", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte("data: d\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.DatabaseAddr(); got != filepath.Join(dir, "d", "launchway.db") {
		t.Fatalf("default %q", got)
	}
	os.WriteFile(path, []byte("database: postgres://db.example/launchway\n"), 0o644)
	c, _ = Load(path)
	if got := c.DatabaseAddr(); got != "postgres://db.example/launchway" {
		t.Fatalf("configured %q", got)
	}
	os.WriteFile(path, []byte("database: other.db\n"), 0o644)
	c, _ = Load(path)
	if got := c.DatabaseAddr(); got != filepath.Join(dir, "other.db") {
		t.Fatalf("relative %q", got)
	}
	t.Setenv("LAUNCHWAY_DATABASE", "postgres://env/launchway")
	if got := c.DatabaseAddr(); got != "postgres://env/launchway" {
		t.Fatalf("env %q", got)
	}
}

func TestRunnerSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte(`
runner:
  secrets:
    mounted: { type: file, dir: secrets, projects: [web-*] }
    vault:   { type: vault, address: "https://vault.example:8200", role: ci, keys: ["apps/*/*"] }
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Runner.Secrets["mounted"]
	if m.Type != "file" || m.Dir != filepath.Join(dir, "secrets") || m.Projects[0] != "web-*" {
		t.Fatalf("mounted %+v", m)
	}
	if v := c.Runner.Secrets["vault"]; v.Role != "ci" || v.Keys[0] != "apps/*/*" {
		t.Fatalf("vault %+v", v)
	}
	os.WriteFile(path, []byte("runner:\n  secrets:\n    v: { type: vault, adress: x }\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "adress") {
		t.Fatalf("a misspelt setting was accepted: %v", err)
	}
}

func TestApprovalsConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte("approvals:\n  allow_self: false\n"), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Approvals.AllowSelf == nil || *c.Approvals.AllowSelf {
		t.Fatalf("allow_self %v", c.Approvals.AllowSelf)
	}
	c, err = Load(filepath.Join(dir, "missing.yaml"))
	if err != nil || c.Approvals.AllowSelf != nil {
		t.Fatalf("default %v %v", c.Approvals.AllowSelf, err)
	}
}

func TestDraftsAndPushSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "launchway.yaml")
	os.WriteFile(path, []byte(`
drafts:
  allow: admin
projects:
  - name: web
    repo: ./web
    push: { branch_prefix: "edits/", allow_direct: ["main", "release/*"] }
  - name: pay
    repo: ./pay
    drafts: { allow: operator, secrets: true }
  - name: docs
    repo: ./docs
    drafts: { allow: none }
`), 0o644)
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := c.DraftRules()
	if err != nil {
		t.Fatal(err)
	}
	for project, want := range map[string]auth.DraftRule{
		"web":   {Allow: auth.Admin},
		"pay":   {Allow: auth.Operator, Secrets: true},
		"docs":  {Allow: auth.NoRole},
		"other": {Allow: auth.Admin},
	} {
		if got := rules.For(project); got != want {
			t.Errorf("%s: %+v, want %+v", project, got, want)
		}
	}
	projects, err := c.SourceProjects()
	if err != nil {
		t.Fatal(err)
	}
	if p := projects[0].Push; p == nil || p.BranchPrefix != "edits/" || strings.Join(p.AllowDirect, ",") != "main,release/*" || projects[1].Push != nil {
		t.Fatalf("push %+v", projects)
	}

	// Without a setting: operators, no secrets.
	empty, _ := Load(filepath.Join(dir, "missing.yaml"))
	if r, _ := empty.DraftRules(); r.For("x") != auth.DefaultDraftRule || auth.DefaultDraftRule != (auth.DraftRule{Allow: auth.Operator}) {
		t.Fatalf("default %+v", r.For("x"))
	}

	for _, bad := range []string{"drafts: { allow: viewer }\n", "drafts: { allow: everyone }\n",
		"projects: [{ name: a, repo: ./a, drafts: { allow: sometimes } }]\n", "projects: [{ name: a, repo: ./a, push: { token: x } }]\n"} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, err := Load(path); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
