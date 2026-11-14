package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"slipway/internal/engine"
	"slipway/internal/executor"
)

// Runner leases steps and executes them, Concurrency at a time.
type Runner struct {
	Client      *Client
	Exec        executor.Executor
	Info        engine.RunnerInfo
	DataDir     string
	Concurrency int
	Log         *slog.Logger

	wsMu    sync.Mutex
	wsLocks map[string]*sync.Mutex
}

// Run takes and executes steps until ctx ends. Steps still running when ctx
// ends are stopped and handed back to the server, which queues them again.
func (r *Runner) Run(ctx context.Context) error {
	if r.Concurrency <= 0 {
		r.Concurrency = 2
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
	r.wsLocks = map[string]*sync.Mutex{}
	abs, err := filepath.Abs(r.DataDir)
	if err != nil {
		return err
	}
	r.DataDir = abs
	if err := os.MkdirAll(r.DataDir, 0o755); err != nil {
		return err
	}
	r.Log.Info("runner started", "name", r.Info.Name, "executor", r.Exec.Name(), "concurrency", r.Concurrency, "server", r.Client.Server)
	go r.collectGarbage(ctx)

	slots := make(chan struct{}, r.Concurrency)
	var wg sync.WaitGroup
	backoff := time.Second
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil
		}
		job, err := r.Client.Lease(ctx, r.Info, 25*time.Second)
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				wg.Wait()
				return nil
			}
			r.Log.Warn("could not reach the server; retrying", "err", err, "in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if job == nil {
			<-slots
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			r.execute(ctx, job)
		}()
	}
}

// logBuffer batches lines and sends them every few hundred milliseconds.
type logBuffer struct {
	mu    sync.Mutex
	lines []engine.LogLine
}

func (b *logBuffer) add(stream, text string) {
	b.mu.Lock()
	b.lines = append(b.lines, engine.LogLine{At: time.Now().UnixMilli(), Stream: stream, Text: text})
	b.mu.Unlock()
}

func (b *logBuffer) take(max int) []engine.LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(b.lines), max)
	out := append([]engine.LogLine(nil), b.lines[:n]...)
	b.lines = b.lines[n:]
	return out
}

func (r *Runner) execute(parent context.Context, job *engine.Job) {
	log := r.Log.With("run", job.RunID, "step", job.StepID, "attempt", job.Attempt)
	// The server talks to the step's lease on a context of its own, so that
	// reporting the result still works while the runner is shutting down.
	report, stopReport := context.WithTimeout(context.Background(), 5*time.Minute)
	defer stopReport()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var (
		mu        sync.Mutex
		requested bool // the run was canceled by someone
		lost      bool // the server gave the step away
	)
	stopWith := func(req, gone bool) {
		mu.Lock()
		requested = requested || req
		lost = lost || gone
		mu.Unlock()
		cancel()
	}

	logs := &logBuffer{}
	sys := func(format string, args ...any) { logs.add("system", fmt.Sprintf(format, args...)) }
	flush := func() {
		for {
			batch := logs.take(engine.MaxLogBatch)
			if len(batch) == 0 {
				return
			}
			if err := r.Client.Logs(report, job.LeaseID, batch); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					stopWith(false, true)
				} else {
					log.Warn("sending logs", "err", err)
				}
				return
			}
		}
	}

	cancelNow, err := r.Client.Start(report, job.LeaseID)
	if err != nil {
		log.Warn("could not start the step", "err", err)
		return
	}
	if cancelNow {
		stopWith(true, false)
	}

	// Heartbeats and log flushing run alongside the step.
	bgDone := make(chan struct{})
	bgStop := make(chan struct{})
	go func() {
		defer close(bgDone)
		hb := time.NewTicker(max(job.LeaseTTL.D()/3, time.Second))
		fl := time.NewTicker(300 * time.Millisecond)
		defer hb.Stop()
		defer fl.Stop()
		for {
			select {
			case <-bgStop:
				return
			case <-fl.C:
				flush()
			case <-hb.C:
				c, err := r.Client.Heartbeat(report, job.LeaseID)
				switch {
				case errors.Is(err, ErrLeaseLost):
					log.Warn("lease lost; stopping the step")
					stopWith(false, true)
				case err != nil:
					log.Warn("heartbeat failed", "err", err)
				case c:
					sys("The run was canceled; stopping.")
					stopWith(true, false)
				}
			}
		}
	}()

	res := r.runStep(ctx, job, sys, logs.add)

	close(bgStop)
	<-bgDone
	mu.Lock()
	wasRequested, wasLost := requested, lost
	mu.Unlock()
	switch {
	case wasLost:
		res = engine.Result{} // the server already moved on; nothing to report
	case wasRequested:
		res = engine.Result{Outcome: "canceled", Error: "canceled"}
	case parent.Err() != nil:
		res = engine.Result{Outcome: "canceled", Error: "the runner is shutting down"}
		sys("The runner is shutting down; the step will be run again.")
	}
	flush()
	if wasLost {
		return
	}
	for i := 0; i < 5; i++ {
		err := r.Client.Finish(report, job.LeaseID, res)
		if err == nil || errors.Is(err, ErrLeaseLost) {
			log.Info("step finished", "outcome", res.Outcome)
			return
		}
		log.Warn("reporting the result failed; retrying", "err", err)
		time.Sleep(time.Duration(i+1) * time.Second)
	}
}

func (r *Runner) runStep(ctx context.Context, job *engine.Job, sys func(string, ...any), out executor.Sink) engine.Result {
	sys("Runner %s, executor %s", r.Info.Name, r.Exec.Name())
	if job.Attempt > 1 {
		sys("Attempt %d", job.Attempt)
	}
	ws, err := r.workspace(ctx, job, sys)
	if err != nil {
		sys("Could not prepare the workspace: %v", err)
		return engine.Result{Outcome: "failed", Error: "preparing the workspace: " + err.Error()}
	}
	meta := filepath.Join(r.DataDir, "steps", job.LeaseID)
	defer os.RemoveAll(meta)
	sys("Image %s", job.Image)
	tctx, cancel := context.WithTimeout(ctx, job.Timeout.D())
	defer cancel()
	start := time.Now()
	code, err := r.Exec.Run(tctx, executor.Spec{
		Name: "slipway-" + job.LeaseID, Image: job.Image, Script: job.Script, Env: job.Env,
		Workspace: ws, MetaDir: meta,
	}, out)
	took := time.Since(start).Round(100 * time.Millisecond)
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		sys("Timed out after %s", job.Timeout.D())
		return engine.Result{Outcome: "failed", Error: fmt.Sprintf("timed out after %s", job.Timeout.D())}
	case err != nil && ctx.Err() != nil:
		return engine.Result{Outcome: "canceled"}
	case err != nil:
		sys("Could not run the step: %v", err)
		return engine.Result{Outcome: "failed", Error: err.Error()}
	}
	outputs := executor.ReadOutputs(meta)
	ec := code
	if code != 0 {
		sys("Failed with exit code %d after %s", code, took)
		return engine.Result{Outcome: "failed", ExitCode: &ec, Error: fmt.Sprintf("exit code %d", code), Outputs: outputs}
	}
	sys("Succeeded in %s", took)
	return engine.Result{Outcome: "succeeded", ExitCode: &ec, Outputs: outputs}
}

// workspace returns the directory a run's steps share on this runner,
// checking out the commit the first time.
func (r *Runner) workspace(ctx context.Context, job *engine.Job, sys func(string, ...any)) (string, error) {
	switch job.Source.Kind {
	case "dir":
		return job.Source.Path, nil
	case "git":
	default:
		return "", fmt.Errorf("unknown source kind %q", job.Source.Kind)
	}
	r.wsMu.Lock()
	lock := r.wsLocks[job.RunID]
	if lock == nil {
		lock = &sync.Mutex{}
		r.wsLocks[job.RunID] = lock
	}
	r.wsMu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	dir := filepath.Join(r.DataDir, "workspaces", job.RunID)
	ready := filepath.Join(r.DataDir, "workspaces", job.RunID+".ready")
	if _, err := os.Stat(ready); err == nil {
		sys("Using the run's workspace from an earlier step")
		return dir, nil
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	sys("Checking out %s at %s", job.Source.URL, short(job.SHA))
	if err := git(ctx, "", "clone", "--quiet", "--no-checkout", job.Source.URL, dir); err != nil {
		return "", err
	}
	if err := git(ctx, dir, "checkout", "--quiet", "--detach", job.SHA); err != nil {
		return "", err
	}
	// Containers may run as another user; let them write to the workspace.
	_ = os.Chmod(dir, 0o777)
	if err := os.WriteFile(ready, nil, 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// collectGarbage removes workspaces nobody has touched for a day.
func (r *Runner) collectGarbage(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		root := filepath.Join(r.DataDir, "workspaces")
		entries, _ := os.ReadDir(root)
		for _, e := range entries {
			info, err := e.Info()
			if err == nil && time.Since(info.ModTime()) > 24*time.Hour {
				_ = os.RemoveAll(filepath.Join(root, e.Name()))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
