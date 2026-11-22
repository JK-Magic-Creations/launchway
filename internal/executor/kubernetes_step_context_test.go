package executor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The build context of an image step is the run's workspace copy as the steps before it left it, with the ignore
// file applied and the size limit measured on what is kept.
func TestTheBuildContextIsWhatEarlierStepsLeftInTheCopy(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"Dockerfile": "FROM scratch\n", "app/main.go": "package main\n"})
	s := stepSpec(t, ws, "run_i", "build", "64Mi", `mkdir -p .cache out; head -c 3000000 /dev/zero > .cache/blob; echo built > out/app; printf '.cache\n' > .dockerignore`)
	if code, text, err := c.run(s); err != nil || code != 0 {
		t.Fatalf("%d %v\n%s", code, err, text)
	}
	dir := c.k.WorkspaceDir(s)
	if dir == "" || !exists(filepath.Join(dir, "out/app")) {
		t.Fatalf("the copy does not hold what the step made: %q", dir)
	}
	cs := ContextSpec{Dir: ".", Dockerfile: "Dockerfile"}
	lim := ExportLimits{MaxBytes: 1 << 20, MaxFiles: 1000}
	if err := CheckLocalContext(dir, cs, lim); err != nil {
		t.Fatalf("with the ignore file the 3 MB cache is not in the context: %v", err)
	}
	os.Remove(filepath.Join(dir, ".dockerignore"))
	if err := CheckLocalContext(dir, cs, lim); !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), ".cache") {
		t.Fatalf("without it the cache is kept and over the limit: %v", err)
	}
	// The checkout does not hold any of it.
	if exists(filepath.Join(ws, "out")) || exists(filepath.Join(ws, ".dockerignore")) {
		t.Fatal("the checkout was written to")
	}
}
