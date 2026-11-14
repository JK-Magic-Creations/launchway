package source

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/launchway-dev/launchway/internal/engine"
	"github.com/launchway-dev/launchway/internal/testdb"
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
	os.MkdirAll(filepath.Join(repo, ".launchway"), 0o755)
	if pipelineYAML != "" {
		os.WriteFile(filepath.Join(repo, ".launchway", "pipeline.yaml"), []byte(pipelineYAML), 0o644)
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

	eng, err := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
	m.poll(ctx, p) // the branch list reuses a fetch from the last few seconds
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

func TestMirrorFollowsAChangedAddress(t *testing.T) {
	dir := t.TempDir()
	repoA, repoB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	for _, r := range []string{repoA, repoB} {
		os.MkdirAll(r, 0o755)
		run(t, r, "git", "init", "-q", "-b", "main")
		commit(t, r, pl, "in "+filepath.Base(r))
	}
	eng, _ := engine.Open(engine.Options{Database: testdb.New(t), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	defer eng.Close()
	m, _ := NewManager(eng, dir, []Project{{Name: "app", URL: repoA}}, nil)
	if _, err := m.Refs(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	// Same data directory, project now pointing somewhere else.
	m2, _ := NewManager(eng, dir, []Project{{Name: "app", URL: repoB}}, nil)
	refs, err := m2.Refs(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if refs[0].Subject != "in b" {
		t.Fatalf("still reading the old repository: %+v", refs[0])
	}
}

// Two servers polling the same repository start exactly one run per push,
// and show the same decisions.
func TestTwoServersStartOneRunPerPush(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	run(t, repo, "git", "init", "-q", "-b", "main")
	commit(t, repo, pl, "first")

	addr := testdb.New(t)
	var managers []*Manager
	for _, name := range []string{"a", "b"} {
		eng, err := engine.Open(engine.Options{Database: addr, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { eng.Close() })
		m, err := NewManager(eng, filepath.Join(dir, name), []Project{{Name: "app", URL: repo}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		managers = append(managers, m)
	}
	pollBoth := func() {
		t.Helper()
		var wg sync.WaitGroup
		for _, m := range managers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := m.poll(context.Background(), m.projects["app"]); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	pollBoth() // first sight: history does not start runs
	pushes := map[string]bool{}
	for i := 0; i < 5; i++ {
		commit(t, repo, "", fmt.Sprintf("push %d", i))
		pollBoth()
		pushes[strings.TrimSpace(gitHead(t, repo))] = true
	}
	run(t, repo, "git", "checkout", "-q", "-b", "feature/x")
	commit(t, repo, "", "on a feature branch")
	pollBoth()

	runs, _ := managers[0].eng.ListRuns(engine.RunFilter{})
	if len(runs) != len(pushes) {
		t.Fatalf("%d runs for %d pushes", len(runs), len(pushes))
	}
	for _, r := range runs {
		if !pushes[r.SHA] {
			t.Fatalf("run for unexpected commit %s", r.SHA)
		}
		delete(pushes, r.SHA) // a second run for the same commit fails here
	}
	a, _ := managers[0].Get("app")
	b, _ := managers[1].Get("app")
	if len(a.Decisions) != 6 || !reflect.DeepEqual(a.Decisions, b.Decisions) {
		t.Fatalf("decisions differ or are missing:\n%+v\n%+v", a.Decisions, b.Decisions)
	}
	if a.Decisions[0].Outcome != "ignored" {
		t.Fatalf("newest decision %+v", a.Decisions[0])
	}
}

func gitHead(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
