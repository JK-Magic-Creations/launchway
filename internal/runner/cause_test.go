package runner

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// stubExec is an executor whose Run ends as the test says.
type stubExec struct {
	code int
	err  error
	wait bool // block until the context ends
}

func (stubExec) Name() string                    { return "stub" }
func (stubExec) WorkspacePath(dir string) string { return dir }
func (s stubExec) Run(ctx context.Context, _ executor.Spec, _ executor.Sink) (int, error) {
	if s.wait {
		<-ctx.Done()
		return -1, ctx.Err()
	}
	return s.code, s.err
}

// The runner says why an attempt failed, in the terms of retry.on, instead
// of leaving the server to guess from the error text.
func TestRunStepReportsTheCause(t *testing.T) {
	for _, tc := range []struct {
		name    string
		src     engine.Source
		exec    stubExec
		timeout time.Duration
		outcome string
		cause   string
	}{
		{"an exit code is the step's", engine.Source{Kind: "dir", Path: t.TempDir()}, stubExec{code: 3}, time.Minute, "failed", pipeline.CauseExit},
		{"a timeout is the step's", engine.Source{Kind: "dir", Path: t.TempDir()}, stubExec{wait: true}, 50 * time.Millisecond, "failed", pipeline.CauseTimeout},
		{"a workspace that cannot be prepared is infrastructure", engine.Source{Kind: "weird"}, stubExec{}, time.Minute, "failed", pipeline.CauseInfrastructure},
		{"an image that cannot be pulled is infrastructure", engine.Source{Kind: "dir", Path: t.TempDir()},
			stubExec{code: -1, err: fmt.Errorf("wrapped: %w", executor.ErrCannotStart)}, time.Minute, "failed", pipeline.CauseInfrastructure},
		{"a pod that was lost is infrastructure", engine.Source{Kind: "dir", Path: t.TempDir()},
			stubExec{code: -1, err: fmt.Errorf("%w: gone", executor.ErrInfrastructure)}, time.Minute, "lost", pipeline.CauseInfrastructure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{Exec: tc.exec, DataDir: t.TempDir(), wsLocks: map[string]*sync.Mutex{}}
			job := &engine.Job{LeaseID: "l1", RunID: "r1", Source: tc.src, Image: "x", Script: "true", Timeout: pipeline.Duration(tc.timeout)}
			res, _ := r.runStep(context.Background(), job, func(string, ...any) {}, func(string, string) {})
			if res.Outcome != tc.outcome || res.Cause != tc.cause {
				t.Fatalf("outcome %q cause %q, want %q %q (%+v)", res.Outcome, res.Cause, tc.outcome, tc.cause, res)
			}
		})
	}
}
