package source

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// launchway run reads a library from a directory, as validate does: the directory is committed to a repository of
// its own, which is what a runner fetches, and the expansion is the one a server would make.
func TestExpandLocalReadsALibraryDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is needed")
	}
	lib := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(lib, "templates"), 0o755))
	must(os.MkdirAll(filepath.Join(lib, "scripts"), 0o755))
	must(os.WriteFile(filepath.Join(lib, "templates", "hello.yaml"), []byte("template: hello\nsteps:\n  say: { image: alpine:3.22, run: \"$LAUNCHWAY_LIB/scripts/say.sh\" }\n"), 0o644))
	must(os.WriteFile(filepath.Join(lib, "scripts", "say.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755))
	p, err := pipeline.Parse([]byte("pipeline: p\nlibraries:\n  mine: { repo: ci, version: v1 }\nsteps:\n  a:\n    uses: mine/hello\n"))
	must(err)
	work := t.TempDir()
	out, libs, err := ExpandLocal(p, map[string]string{"mine": lib}, work)
	if err != nil {
		t.Fatal(err)
	}
	if out.Step("a/say") == nil || out.Step("a/say").Library != "mine" {
		t.Fatalf("steps: %+v", out.Steps)
	}
	if len(libs) != 1 || libs[0].Name != "mine" || !pipeline.IsCommitID(libs[0].Commit) || !strings.HasPrefix(libs[0].URL, work) {
		t.Fatalf("libraries: %+v", libs)
	}
	if b, err := exec.Command("git", "-C", libs[0].URL, "show", libs[0].Commit+":scripts/say.sh").Output(); err != nil || !strings.Contains(string(b), "echo hi") {
		t.Errorf("the committed library lacks its script: %v %s", err, b)
	}
	// A library with no directory is refused with the flag to add; a directory without templates, too.
	p, _ = pipeline.Parse([]byte("pipeline: p\nlibraries:\n  mine: { repo: ci, version: v1 }\nsteps:\n  a:\n    uses: mine/hello\n"))
	if _, _, err := ExpandLocal(p, nil, work); err == nil || !strings.Contains(err.Error(), "--library mine=DIRECTORY") {
		t.Errorf("%v", err)
	}
	p, _ = pipeline.Parse([]byte("pipeline: p\nlibraries:\n  mine: { repo: ci, version: v1 }\nsteps:\n  a:\n    uses: mine/hello\n"))
	if _, _, err := ExpandLocal(p, map[string]string{"mine": t.TempDir()}, work); err == nil || !strings.Contains(err.Error(), "no templates directory") {
		t.Errorf("%v", err)
	}
	// The starters need no directory.
	p, _ = pipeline.Parse([]byte("pipeline: p\nsteps:\n  ci:\n    uses: launchway/go-check\n"))
	if _, libs, err := ExpandLocal(p, nil, work); err != nil || len(libs) != 1 || libs[0].Name != "launchway" {
		t.Errorf("%v %+v", err, libs)
	}
}
