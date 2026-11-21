package gitfs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadsOnlyWhatMatters(t *testing.T) {
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@e.com", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	work := filepath.Join(root, "w")
	os.MkdirAll(work, 0o755)
	run(work, "init", "-q")
	files := map[string]string{"go.mod": "module x\n", "web/package.json": "{}", "README.md": "no", "src/main.go": "package main", "Dockerfile": "FROM a\n", ".launchway/updates.yaml": "version: 1\n", ".launchway/pipeline.yaml": "x", "scripts/lib.sh": "A_IMAGE=x", ".github/workflows/ci.yml": "x", "big/package.json": strings.Repeat("x", 100)}
	for p, c := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(work, p)), 0o755)
		os.WriteFile(filepath.Join(work, p), []byte(c), 0o644)
	}
	run(work, "add", "-A")
	run(work, "commit", "-q", "-m", "x")
	gitDir := filepath.Join(work, ".git")
	c, err := Commit(context.Background(), gitDir, "main")
	if err != nil || len(c) != 40 {
		t.Fatalf("%q %v", c, err)
	}
	if _, err := Commit(context.Background(), gitDir, "nope"); err == nil {
		t.Error("unknown branch")
	}
	fs, notes, err := Read(context.Background(), gitDir, c, []string{"scripts/*.sh"}, Limits{MaxFileSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"go.mod", "web/package.json", "Dockerfile", ".launchway/updates.yaml", ".launchway/pipeline.yaml", "scripts/lib.sh", ".github/workflows/ci.yml"} {
		if _, ok := fs[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{"README.md", "src/main.go", "big/package.json"} {
		if _, ok := fs[not]; ok {
			t.Errorf("%s must not be read", not)
		}
	}
	if string(fs["go.mod"].Data) != "module x\n" || len(notes) != 1 || !strings.Contains(notes[0], "big/package.json") {
		t.Errorf("%q %v", fs["go.mod"].Data, notes)
	}
}
