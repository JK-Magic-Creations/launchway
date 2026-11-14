package source

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"slipway/internal/engine"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Dana", "GIT_AUTHOR_EMAIL=d@example.com",
		"GIT_COMMITTER_NAME=Dana", "GIT_COMMITTER_EMAIL=d@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

func commit(t *testing.T, repo, pipelineYAML, msg string) {
	t.Helper()
	os.MkdirAll(filepath.Join(repo, ".slipway"), 0o755)
	if pipelineYAML != "" {
		os.WriteFile(filepath.Join(repo, ".slipway", "pipeline.yaml"), []byte(pipelineYAML), 0o644)
	}
	os.WriteFile(filepath.Join(repo, "change.txt"), []byte(msg), 0o644)
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "commit", "-q", "-m", msg)
}

const pl = `
pipeline: app
on:
  push: { branches: [main] }
steps:
  test: {image: alpine, run: echo ok}
`

func TestPollingStartsRunsAndExplainsDecisions(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "git", "init", "-q", "-b", "main")
	commit(t, repo, pl, "first")

	eng, err := engine.Open(engine.Options{Path: filepath.Join(dir, "db"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	m, err := NewManager(eng, dir, []Project{{Name: "app", URL: repo}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := m.projects["app"]
	ctx := context.Background()

	// First sight: history does not start runs.
	if err := m.poll(ctx, p); err != nil {
		t.Fatal(err)
	}
	if runs, _ := eng.ListRuns(engine.RunFilter{}); len(runs) != 0 {
		t.Fatalf("history started %d runs", len(runs))
	}

	// A push to main starts a run.
	commit(t, repo, "", "second")
	m.poll(ctx, p)
	runs, _ := eng.ListRuns(engine.RunFilter{})
	if len(runs) != 1 || runs[0].Event != "push" || runs[0].Branch != "main" || runs[0].Message != "second" || runs[0].Author != "Dana" {
		t.Fatalf("runs %+v", runs)
	}

	// A push to another branch is recorded as ignored, with the reason.
	run(t, repo, "git", "checkout", "-q", "-b", "feature/x")
	commit(t, repo, "", "on a feature branch")
	m.poll(ctx, p)
	if runs, _ := eng.ListRuns(engine.RunFilter{}); len(runs) != 1 {
		t.Fatalf("a feature push started a run")
	}
	st, _ := m.Get("app")
	if st.Decisions[0].Outcome != "ignored" || !strings.Contains(st.Decisions[0].Detail, "does not match") {
		t.Fatalf("decision %+v", st.Decisions[0])
	}

	// A broken pipeline file is reported, not silently dropped.
	run(t, repo, "git", "checkout", "-q", "main")
	commit(t, repo, "pipeline: app\nsteps:\n  test: {image: alpine}\n", "break it")
	m.poll(ctx, p)
	st, _ = m.Get("app")
	if st.Decisions[0].Outcome != "error" || !strings.Contains(st.Decisions[0].Detail, "a script to run is required") {
		t.Fatalf("decision %+v", st.Decisions[0])
	}

	// Branches are listed, and a run can be started by hand on any of them.
	refs, err := m.Refs(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, r := range refs {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "main,feature/x" {
		t.Fatalf("refs %v", names)
	}
	r, err := m.StartRun(ctx, "app", "feature/x", "tester")
	if err != nil {
		t.Fatal(err)
	}
	if r.Event != "manual" || r.Branch != "feature/x" || r.Actor != "tester" {
		t.Fatalf("manual run %+v", r)
	}
	if _, err := m.StartRun(ctx, "app", "main", "tester"); err == nil || !strings.Contains(err.Error(), "a script to run is required") {
		t.Fatalf("starting a broken pipeline should explain why: %v", err)
	}
	if _, err := m.StartRun(ctx, "app", "nope", "tester"); err == nil {
		t.Fatal("expected an error for a missing branch")
	}

	// Tags work too.
	run(t, repo, "git", "tag", "-a", "v1.0.0", "-m", "release", "HEAD~1")
	refs, _ = m.Refs(ctx, "app")
	found := false
	for _, r := range refs {
		if r.Kind == "tag" && r.Name == "v1.0.0" && r.Subject == "second" {
			found = true
		}
	}
	if !found {
		t.Fatalf("annotated tag not resolved to its commit: %+v", refs)
	}
}
