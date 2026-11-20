package source

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/egress"
)

// By default the check reads no path on the server and no internal
// address, and says so in a few generic words, never with what Git or the
// network answered.
func TestCheckRepositoryRefusesLocalAndInternalAddresses(t *testing.T) {
	dir := t.TempDir()
	checkGit(t, dir, "init", "-q", "-b", "main")
	for _, addr := range []string{dir, "file://" + dir, "http://127.0.0.1:9/x.git", "http://localhost/x.git",
		"https://169.254.169.254/latest", "git://10.1.2.3/x.git", "ssh://git@192.168.1.5/x.git", "git@[::1]:x.git"} {
		got := CheckRepository(context.Background(), addr, "")
		if got.OK || !got.Blocked || got.Problem == "" || len(got.Problem) > 300 {
			t.Errorf("%s: %+v", addr, got)
		}
		if strings.Contains(got.Problem, dir) || strings.Contains(got.Problem, "fatal") {
			t.Errorf("%s: the answer repeats detail: %s", addr, got.Problem)
		}
	}
	if got := CheckRepository(context.Background(), "http://127.0.0.1:9/x.git", ""); got.BlockedHost != "127.0.0.1" {
		t.Errorf("the host to allow is not named: %+v", got)
	}
	// Once an administrator lists the host, it is read (here, refused by
	// the closed port, which is not the guard).
	old := egress.Default.Policy()
	_ = egress.Default.Set(egress.Policy{Allow: []string{"127.0.0.0/8"}})
	defer egress.Default.Set(old)
	if got := CheckRepository(context.Background(), "http://127.0.0.1:9/x.git", ""); got.Blocked {
		t.Errorf("an allowed host is still refused: %+v", got)
	}
}

// The mirrors the server polls are guarded as the check is: a network
// address that is internal is never fetched, and Git is told not to follow
// redirects or use any protocol the policy does not allow.
func TestMirrorsAreGuarded(t *testing.T) {
	ctx := context.Background()
	if _, err := remoteEnv(ctx, "http://10.0.0.7/x.git"); err == nil {
		t.Error("an internal address would be fetched")
	}
	env, err := remoteEnv(ctx, "/srv/git/x") // allowed when configured
	if err != nil || env != nil {
		t.Errorf("a path: %v %v", env, err)
	}
	p := &project{Project: Project{Name: "x", URL: "http://169.254.169.254/x.git"}, dir: filepath.Join(t.TempDir(), "m"), mirror: &mirror{}}
	if err := p.fetch(ctx, 0); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("fetch: %v", err)
	}
}

func checkGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// allowLocalPaths lets a test use repositories on this machine, as a server
// started with egress.allow_local does.
func allowLocalPaths(t *testing.T) {
	t.Helper()
	old := egress.Default.Policy()
	_ = egress.Default.Set(egress.Policy{AllowLocal: true})
	t.Cleanup(func() { _ = egress.Default.Set(old) })
}

func TestCheckRepository(t *testing.T) {
	allowLocalPaths(t)
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
