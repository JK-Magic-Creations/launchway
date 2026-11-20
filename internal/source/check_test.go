package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func checkGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCheckRepository(t *testing.T) {
	dir := t.TempDir()
	checkGit(t, dir, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(dir, ".launchway"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".launchway", "pipeline.yaml"), []byte("pipeline: x\nsteps:\n  a:\n    image: none\n    run: echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkGit(t, dir, "add", ".")
	checkGit(t, dir, "commit", "-q", "-m", "first")
	checkGit(t, dir, "branch", "dev")

	got := CheckRepository(context.Background(), dir, "")
	if !got.OK || got.DefaultBranch != "main" || len(got.Branches) != 2 || !got.HasPipelineFile || !got.Valid {
		t.Errorf("a repository with a pipeline file: %+v", got)
	}
	if got.PipelineFile != ".launchway/pipeline.yaml" || got.Commit == "" {
		t.Errorf("the file and commit are not reported: %+v", got)
	}
	// A pipeline with a mistake: its file, line and commit are reported.
	if err := os.WriteFile(filepath.Join(dir, ".launchway", "pipeline.yaml"), []byte("pipeline: x\nsteps:\n  a:\n    run: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checkGit(t, dir, "commit", "-q", "-am", "break it")
	if bad := CheckRepository(context.Background(), dir, "main"); !bad.OK || bad.Valid || len(bad.Problems) == 0 ||
		bad.Problems[0].File != ".launchway/pipeline.yaml" || bad.Problems[0].Line == 0 || bad.Problems[0].Commit != bad.Commit {
		t.Errorf("a broken pipeline: %+v", bad)
	}
	got = CheckRepository(context.Background(), dir, "nope")
	if got.OK || got.Problem == "" {
		t.Errorf("a missing branch was accepted: %+v", got)
	}
	got = CheckRepository(context.Background(), filepath.Join(dir, "missing"), "")
	if got.OK || got.Problem == "" {
		t.Errorf("a missing repository was accepted: %+v", got)
	}
	for _, bad := range []string{"", "--upload-pack=x", "ext::sh -c id"} {
		if got := CheckRepository(context.Background(), bad, ""); got.OK || got.Problem == "" {
			t.Errorf("%q was accepted: %+v", bad, got)
		}
	}
	empty := t.TempDir()
	checkGit(t, empty, "init", "-q", "-b", "main")
	if got := CheckRepository(context.Background(), empty, ""); got.OK || got.Problem == "" {
		t.Errorf("a repository without branches was accepted: %+v", got)
	}
}
