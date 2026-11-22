package service

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/source"
	"github.com/jk-magic-creations/launchway/internal/standards"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

func newService(t *testing.T) *Service {
	t.Helper()
	e, err := engine.Open(engine.Options{Database: testdb.New(t), LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return &Service{Eng: e, Now: func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC) }}
}

// repo makes a Git repository with the given files on main and returns its
// directory and the commit.
func repo(t *testing.T, files map[string]string) (dir, sha string) {
	t.Helper()
	home, _ := os.UserHomeDir()
	dir, err := os.MkdirTemp(filepath.Join(home, "scratch"), "standards-svc-")
	if err != nil {
		dir = t.TempDir()
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("add", "-A")
	git("commit", "-q", "-m", "first")
	return filepath.Join(dir, ".git"), git("rev-parse", "HEAD")
}

const basePipeline = "pipeline: app\non: { push: { branches: [main] }, pull_request: {} }\nsteps:\n  build: {image: docker.io/library/alpine:3.22, run: make}\n"

func inspect(t *testing.T, s *Service, gitDir, sha string, onDefault bool) *pipeline.Pipeline {
	t.Helper()
	p, err := pipeline.Parse([]byte(basePipeline))
	if err != nil {
		t.Fatal(err)
	}
	ev := pipeline.EventPullRequest
	if onDefault {
		ev = pipeline.EventPush
	}
	q, _, err := s.Inspect(context.Background(), source.InspectRequest{Project: "app", GitDir: gitDir, SHA: sha, DefaultBranch: "main", OnDefaultBranch: onDefault, Event: ev}, p)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func set(t *testing.T, s *Service, level, scope string, v standards.Settings) {
	t.Helper()
	_, ver, _ := s.Doc(level, scope)
	raw, _ := json.Marshal(v)
	if _, _, err := s.Eng.SetStandingSettings(level, scope, raw, ver, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestEveryRunGainsTheInspectionWithNoChangeToTheRepository(t *testing.T) {
	s := newService(t)
	dir, sha := repo(t, map[string]string{"go.mod": "module x\n\ngo 1.22\n", "main.go": "package main\n", "Dockerfile": "FROM scratch\n", "README.md": "x\n"})
	q := inspect(t, s, dir, sha, false)
	for _, id := range []string{"inspect-secrets", "inspect-vuln-deps", "inspect-vuln-reach-go", "inspect-sast-go", "inspect-dockerfile", "inspect-license", "inspect-sbom", "inspect-misconfig", "inspection"} {
		if q.Step(id) == nil {
			t.Errorf("no step %s", id)
		}
	}
	if q.Step("inspect-sast-python") != nil || q.Step("inspect-shell") != nil {
		t.Error("a check for an ecosystem the repository does not have was added")
	}
	g := q.Step("inspection")
	var pol standards.Policy
	if err := json.Unmarshal(g.Gate.Policy, &pol); err != nil || pol.DefaultBranchRun || pol.FailOn != "high" {
		t.Fatalf("%+v %v", pol, err)
	}
	if st := q.Step("inspect-secrets"); st.Env["LAUNCHWAY_STANDING_SINCE"] != "" {
		t.Error("the first Inspection of a project must read every commit; it was limited to part of the history")
	}
	if len(q.Steps) != 1+len(pol.Checks)+1 {
		t.Errorf("%d steps for %d checks", len(q.Steps), len(pol.Checks))
	}
	if q := inspect(t, s, dir, sha, true); !func() bool {
		var p standards.Policy
		_ = json.Unmarshal(q.Step("inspection").Gate.Policy, &p)
		return p.DefaultBranchRun
	}() {
		t.Error("a run on the default branch is not marked so")
	}
}

func TestLevelsFileAndLocksTogether(t *testing.T) {
	s := newService(t)
	file := "checks:\n  secrets: advise\n  sast-go: off\ngate:\n  fail_on: critical\nignore:\n  - rule: G401\n    reason: weak hash used for cache keys only\n    expires: 2027-02-01\n"
	dir, sha := repo(t, map[string]string{"go.mod": "module x\n", "main.go": "package main\n", ".launchway/standards.yaml": file})
	q := inspect(t, s, dir, sha, false)
	if q.Step("inspect-sast-go") != nil {
		t.Error("the repository file turned sast-go off, but it is still there")
	}
	var pol standards.Policy
	_ = json.Unmarshal(q.Step("inspection").Gate.Policy, &pol)
	if pol.FailOn != "critical" || len(pol.Exemptions) != 1 {
		t.Fatalf("the file's thresholds and exemptions did not apply: %+v", pol)
	}
	// The Wing locks secrets at block and the gate at high: the file cannot weaken them.
	set(t, s, standards.LevelOrganization, "default", standards.Settings{
		Checks: map[string]standards.State{"secrets": standards.Block}, Gate: standards.GateSettings{FailOn: "high"},
		Locks: map[string]string{standards.CheckKey("secrets"): "the security team requires it", standards.KeyFailOn: "the security team requires it", standards.KeyIgnore: "no exemptions here"}})
	q = inspect(t, s, dir, sha, false)
	pol = standards.Policy{}
	_ = json.Unmarshal(q.Step("inspection").Gate.Policy, &pol)
	var secrets standards.State
	for _, c := range pol.Checks {
		if c.ID == "secrets" {
			secrets = c.State
		}
	}
	if secrets != standards.Block || pol.FailOn != "high" || len(pol.Exemptions) != 0 {
		t.Errorf("the file weakened what the Wing locked: secrets=%s fail_on=%s exemptions=%d", secrets, pol.FailOn, len(pol.Exemptions))
	}
	eff, err := s.Effective("app", []string{"go.mod"}, mustFile(t, file), nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(eff.Notices, "\n")
	if !strings.Contains(joined, "locked by the Wing") || !strings.Contains(joined, "the security team requires it") {
		t.Errorf("the settings page would not say why:\n%s", joined)
	}
	// A project with the single off switch turned on (and a reason) gets no Inspection...
	set(t, s, standards.LevelOrganization, "default", standards.Settings{})
	off := true
	set(t, s, standards.LevelProject, "app", standards.Settings{Off: &off, OffReason: "moving to another scanner this month"})
	q = inspect(t, s, dir, sha, false)
	if q.Step("inspection") != nil || len(q.Steps) != 1 {
		t.Errorf("an Inspection was added while Standing orders are off: %d steps", len(q.Steps))
	}
	// ...unless a Wing locked it on.
	set(t, s, standards.LevelOrganization, "default", standards.Settings{Off: new(bool), Locks: map[string]string{standards.KeyOff: "never off in this Wing"}})
	if q = inspect(t, s, dir, sha, false); q.Step("inspection") == nil {
		t.Error("a project switched Standing orders off past the Wing's lock")
	}
}

func mustFile(t *testing.T, text string) *standards.Settings {
	t.Helper()
	set, ps := standards.ParseFile([]byte(text), time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	if len(ps) > 0 {
		t.Fatal(ps)
	}
	return &set
}

// The file is read from the default branch, never from the commit that is
// being inspected: a pull request cannot switch its own checks off.
func TestAPullRequestCannotTurnItsOwnChecksOff(t *testing.T) {
	s := newService(t)
	dir, sha := repo(t, map[string]string{"go.mod": "module x\n", "main.go": "package main\n"})
	// A second commit on a branch adds a file that turns everything off.
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = filepath.Dir(dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	}
	run("checkout", "-q", "-b", "evil")
	_ = os.MkdirAll(filepath.Join(filepath.Dir(dir), ".launchway"), 0o755)
	_ = os.WriteFile(filepath.Join(filepath.Dir(dir), ".launchway", "standards.yaml"), []byte("off: true\noff_reason: \"this change is a hurry\"\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "turn it off")
	out, _ := exec.Command("git", "-C", filepath.Dir(dir), "rev-parse", "HEAD").Output()
	evil := strings.TrimSpace(string(out))
	if evil == sha {
		t.Fatal("no second commit")
	}
	if q := inspect(t, s, dir, evil, false); q.Step("inspection") == nil {
		t.Error("a pull request switched the Inspection off for itself")
	}
}

// An installation can switch the Inspection off with one environment setting before it upgrades.
func TestTheInstallationCanSwitchTheInspectionOffWithOneSetting(t *testing.T) {
	s := newService(t)
	dir, sha := repo(t, map[string]string{"go.mod": "module x\n\ngo 1.22\n", "main.go": "package main\n"})
	t.Setenv(OffEnv, "off")
	p, err := pipeline.Parse([]byte(basePipeline))
	if err != nil {
		t.Fatal(err)
	}
	q, note, err := s.Inspect(context.Background(), source.InspectRequest{Project: "app", GitDir: dir, SHA: sha, DefaultBranch: "main", Event: pipeline.EventPullRequest}, p)
	if err != nil || len(q.Steps) != len(p.Steps) || q.Step("inspection") != nil || !strings.Contains(note, OffEnv) {
		t.Fatalf("steps %d (want %d), note %q, err %v", len(q.Steps), len(p.Steps), note, err)
	}
	t.Setenv(OffEnv, "")
	if q := inspect(t, s, dir, sha, false); q.Step("inspection") == nil {
		t.Error("with the setting empty the Inspection must be added")
	}
}
