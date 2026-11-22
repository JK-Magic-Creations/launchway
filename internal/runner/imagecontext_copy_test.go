package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
)

// dirExec is an executor, such as a Kubernetes Station with a pod for each step, whose run workspace is a copy on
// the Station that earlier steps wrote into, not the checkout.
type dirExec struct {
	stubExec
	dir string
}

func (e dirExec) WorkspaceDir(executor.Spec) string { return e.dir }

// An image step reads its build context from the run's workspace on the Station, as the steps before it left it
// (an earlier step wrote the ignore file and a large cache), not from the checkout; the ignore file still keeps
// the cache out, and the size limit is measured on what is kept.
func TestBuildContextComesFromTheStationsCopyOfTheWorkspace(t *testing.T) {
	checkout, copyDir := t.TempDir(), t.TempDir()
	write := func(dir, name string, content []byte) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(checkout, "Dockerfile", []byte("FROM scratch\n"))
	write(checkout, "src/a.go", make([]byte, 20))
	// What the steps before left in the copy: the checkout's files, changed, and a 3 MiB cache.
	write(copyDir, "Dockerfile", []byte("FROM scratch\nCOPY out /out\n"))
	write(copyDir, "src/a.go", make([]byte, 20))
	write(copyDir, "out/built", []byte("made by an earlier step"))
	write(copyDir, ".cache/blob", make([]byte, 3<<20))
	write(copyDir, ".dockerignore", []byte(".cache\n"))

	r := &Runner{Exec: dirExec{dir: copyDir}, Images: &imagebuild.Config{MaxContextMiB: 1}}
	job := &engine.Job{ImageBuild: &engine.ImageJob{Context: ".", File: "Dockerfile"}}
	var said []string
	ctxDir, df, err := r.buildContext(context.Background(), job, executor.Spec{}, checkout, t.TempDir(), func(f string, a ...any) { said = append(said, f) })
	if err != nil {
		t.Fatalf("the ignore file keeps the cache out: %v", err)
	}
	want, _ := filepath.EvalSymlinks(copyDir)
	if got, _ := filepath.EvalSymlinks(ctxDir); got != want {
		t.Fatalf("the context is %s, not the Station's copy %s", ctxDir, want)
	}
	if b, _ := os.ReadFile(df); !strings.Contains(string(b), "COPY out /out") {
		t.Fatalf("the Dockerfile is the checkout's, not the one the earlier step changed: %q", b)
	}
	if len(said) == 0 || !strings.Contains(said[0], "from the run's workspace on the Station") {
		t.Fatalf("the log does not say where the context came from: %v", said)
	}
	// Take the ignore file away and the same cache is kept: over the limit, with the directory named.
	if err := os.Remove(filepath.Join(copyDir, ".dockerignore")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.buildContext(context.Background(), job, executor.Spec{}, checkout, t.TempDir(), func(string, ...any) {}); !errors.Is(err, executor.ErrTooLarge) || !strings.Contains(err.Error(), ".cache") {
		t.Fatalf("without the ignore file: %v", err)
	}
	// A run with no copy yet (its first step) uses the checkout.
	r = &Runner{Exec: dirExec{}, Images: &imagebuild.Config{MaxContextMiB: 1}}
	ctxDir, _, err = r.buildContext(context.Background(), job, executor.Spec{}, checkout, t.TempDir(), func(string, ...any) {})
	if got, _ := filepath.EvalSymlinks(ctxDir); err != nil || got != func() string { p, _ := filepath.EvalSymlinks(checkout); return p }() {
		t.Fatalf("the first step: %s %v", ctxDir, err)
	}
}
