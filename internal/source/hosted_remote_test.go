package source

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The server's image has no shell, and git reads a plain local path through
// one. localRemote must therefore never hand git a plain path, and must work
// for a directory whose name holds a space or a percent sign.
func TestLocalRemoteNeedsNoShellAndSurvivesOddNames(t *testing.T) {
	if got := localRemote("/var/lib/launchway/repos/rep_1.git"); got != "ext::git-upload-pack /var/lib/launchway/repos/rep_1.git" {
		t.Fatalf("remote = %q", got)
	}
	ctx := context.Background()
	root := t.TempDir()
	src := filepath.Join(root, "a b", "100%.git")
	if err := os.MkdirAll(filepath.Dir(src), 0o750); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "work")
	for _, c := range [][]string{
		{"init", "--quiet", "--bare", src},
		{"init", "--quiet", work},
		{"-C", work, "-c", "user.name=a", "-c", "user.email=a@example.com", "commit", "--quiet", "--allow-empty", "-m", "one"},
		{"-C", work, "push", "--quiet", src, "HEAD:refs/heads/main"},
	} {
		if _, err := gitRun(ctx, "", nil, "", c...); err != nil {
			t.Fatalf("git %v: %v", c, err)
		}
	}
	dst := filepath.Join(root, "dst.git")
	if _, err := gitRun(ctx, "", nil, "", "-c", "protocol.ext.allow=always", "clone", "--mirror", "--quiet", localRemote(src), dst); err != nil {
		t.Fatalf("clone through the shell-free remote: %v", err)
	}
	if _, err := gitRun(ctx, dst, nil, "", "-c", "protocol.ext.allow=always", "fetch", "--quiet", "--prune", localRemote(src), "+refs/heads/*:refs/heads/*"); err != nil {
		t.Fatalf("fetch through the shell-free remote: %v", err)
	}
	out, err := gitRun(ctx, dst, nil, "", "for-each-ref", "--format=%(refname)")
	if err != nil || out == "" {
		t.Fatalf("no refs arrived: %q %v", out, err)
	}
}
