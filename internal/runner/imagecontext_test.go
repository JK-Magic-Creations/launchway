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

// exporter is an executor with a pod, whose export says the context is over the limit.
type exporter struct {
	stubExec
	got executor.ContextSpec
	err error
}

func (e *exporter) ExportContext(_ context.Context, _ executor.Spec, c executor.ContextSpec, dst string, _ executor.ExportLimits) (bool, error) {
	e.got = c
	return true, e.err
}
func (e *exporter) ExportFile(context.Context, executor.Spec, string, string, int64) (bool, error) {
	return true, nil
}

// The executor says what it kept, and the step fails with that sentence as it
// is: it already names the ignore file, the sizes and the directories.
func TestBuildContextFromAPodPassesTheTooLargeMessageOn(t *testing.T) {
	tooLarge := &executor.ContextTooLarge{Context: "app", Ignore: ".dockerignore", Kept: 3 << 30, KeptFiles: 9, MaxBytes: 2 << 30}
	ex := &exporter{err: tooLarge}
	r := &Runner{Exec: ex, Images: &imagebuild.Config{}}
	job := &engine.Job{ImageBuild: &engine.ImageJob{Context: "app", File: "app/Dockerfile"}}
	_, _, err := r.buildContext(context.Background(), job, executor.Spec{}, t.TempDir(), t.TempDir(), func(string, ...any) {})
	if !errors.Is(err, executor.ErrTooLarge) || err.Error() != tooLarge.Error() {
		t.Fatalf("%v", err)
	}
	if ex.got.Dir != "app" || ex.got.Dockerfile != "app/Dockerfile" {
		t.Fatalf("the executor was asked for %+v", ex.got)
	}
}

// With the context on the runner's own disk the limit applies to what the
// ignore file keeps, as it does for a copy out of a pod.
func TestBuildContextOnDiskAppliesTheLimitAfterTheIgnoreFile(t *testing.T) {
	ws := t.TempDir()
	write := func(name string, n int) {
		p := filepath.Join(ws, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", 20)
	write("src/a.go", 20)
	write(".cache/blob", 3<<20)
	r := &Runner{Exec: stubExec{}, Images: &imagebuild.Config{MaxContextMiB: 1}}
	job := &engine.Job{ImageBuild: &engine.ImageJob{Context: ".", File: "Dockerfile"}}
	call := func() error {
		_, _, err := r.buildContext(context.Background(), job, executor.Spec{}, ws, t.TempDir(), func(string, ...any) {})
		return err
	}
	if err := call(); !errors.Is(err, executor.ErrTooLarge) || !strings.Contains(err.Error(), ".cache") {
		t.Fatalf("without an ignore file: %v", err)
	}
	write(".dockerignore", 0)
	if err := os.WriteFile(filepath.Join(ws, ".dockerignore"), []byte(".cache\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := call(); err != nil {
		t.Fatalf("with the ignore file: %v", err)
	}
}
