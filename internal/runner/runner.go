package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/redact"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// Runner leases steps and executes them, Concurrency at a time.
type Runner struct {
	Client      *Client
	Exec        executor.Executor
	Info        engine.RunnerInfo
	DataDir     string
	Concurrency int
	Log         *slog.Logger
	// Secrets resolves the secrets steps ask for; nil gives the default
	// env store only.
	Secrets *secrets.Resolver
	// SecretWait is how long to keep asking an unavailable secret store
	// before handing the step back to be run again; default 30s.
	SecretWait time.Duration

	wsMu    sync.Mutex
	wsLocks map[string]*sync.Mutex
	// One fetch at a time per library mirror; see libraryRepo.
	libMu    sync.Mutex
	libLocks map[string]*sync.Mutex
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
	if r.Secrets == nil {
		r.Secrets = secrets.NewResolver()
	}
	if r.SecretWait <= 0 {
		r.SecretWait = 30 * time.Second
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
	// The first request does not wait for work, so that whether the server
	// can be reached is known at once (see Health).
	wait := time.Duration(0)
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			r.leave(&wg)
			return nil
		}
		job, err := r.Client.Lease(ctx, r.Info, wait)
		wait = 25 * time.Second
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				r.leave(&wg)
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

// leave waits for the steps in progress to be handed back, then tells the
// server this runner is gone, so that its runs move to other runners at once.
func (r *Runner) leave(steps *sync.WaitGroup) {
	steps.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Client.Leave(ctx, r.Info.ID); err != nil {
		r.Log.Warn("could not tell the server this runner is leaving; its runs move to other runners once it is missed", "err", err)
	}
}

// Health serves /healthz, which answers while the process is up, and
// /readyz, which answers 200 only if the server answered within the last
// two minutes. A runner waiting for work asks the server at least every
// 25 seconds, and one running steps sends heartbeats more often than that.
func (r *Runner) Health() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		last := r.Client.LastContact()
		if last.IsZero() {
			http.Error(w, "the server has not answered yet", http.StatusServiceUnavailable)
			return
		}
		if ago := time.Since(last); ago > readyWithin {
			http.Error(w, fmt.Sprintf("the server last answered %s ago", ago.Round(time.Second)), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// readyWithin is how recently the server must have answered for a runner to
// be ready.
const readyWithin = 2 * time.Minute

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
	if job.TakenOverFrom != "" {
		sys("This run moves here from runner %s, which stopped responding or left. It continues in a fresh workspace: files that earlier steps wrote are not in it.", job.TakenOverFrom)
	}
	if job.Attempt > 1 {
		sys("Attempt %d", job.Attempt)
	}
	values, failed := r.resolveSecrets(ctx, job, sys)
	if failed != nil {
		return *failed
	}
	mask, short := secrets.NewMasker(values)
	for _, name := range short {
		sys("%s has a value, or a line of one, shorter than %d characters; it is not masked in the output", name, secrets.MinMaskLen)
	}
	env := make(map[string]string, len(job.Env)+len(values))
	maps.Copy(env, job.Env)
	maps.Copy(env, values)

	ws, err := r.workspace(ctx, job, sys)
	if err != nil {
		sys("Could not prepare the workspace: %v", err)
		return engine.Result{Outcome: "failed", Error: "preparing the workspace: " + err.Error()}
	}
	libraryEnv(env, job, r.Exec.WorkspacePath(ws))
	meta := filepath.Join(r.DataDir, "steps", job.LeaseID)
	defer os.RemoveAll(meta)
	sys("Image %s", job.Image)
	tctx, cancel := context.WithTimeout(ctx, job.Timeout.D())
	defer cancel()
	start := time.Now()
	// The last lines of output, already masked, say why a failed step failed.
	tail := &outputTail{}
	// The executor masks stdout and stderr before splitting long lines; the
	// sink masks again so that nothing reaches the server unmasked even if
	// an executor does not.
	code, err := r.Exec.Run(tctx, executor.Spec{
		RunID: job.RunID, Containers: containerSpecs(job), Container: job.Container,
		Name: "launchway-" + job.LeaseID, Image: job.Image, Script: job.Script, Env: env,
		Workspace: ws, WorkspaceID: workspaceID(job), MetaDir: meta, Mask: mask.Mask,
	}, func(stream, text string) {
		if stream == "system" {
			sys("%s", mask.Mask(text))
		} else {
			masked := mask.Mask(text)
			tail.add(stream, masked)
			out(stream, masked)
		}
	})
	took := time.Since(start).Round(100 * time.Millisecond)
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		sys("Timed out after %s", job.Timeout.D())
		return engine.Result{Outcome: "failed", Error: fmt.Sprintf("timed out after %s", job.Timeout.D()), Excerpt: tail.excerpt()}
	case err != nil && ctx.Err() != nil:
		return engine.Result{Outcome: "canceled"}
	case errors.Is(err, executor.ErrInfrastructure):
		msg := mask.Mask(err.Error())
		sys("%s. The step will be run again.", msg)
		return engine.Result{Outcome: "lost", Error: msg}
	case err != nil:
		msg := mask.Mask(err.Error())
		sys("Could not run the step: %s", msg)
		return engine.Result{Outcome: "failed", Error: msg}
	}
	outputs := maskOutputs(executor.ReadOutputs(meta), mask)
	ec := code
	if code != 0 {
		sys("Failed with exit code %d after %s", code, took)
		return engine.Result{Outcome: "failed", ExitCode: &ec, Error: fmt.Sprintf("exit code %d", code), Outputs: outputs,
			Excerpt: tail.excerpt()}
	}
	sys("Succeeded in %s", took)
	return engine.Result{Outcome: "succeeded", ExitCode: &ec, Outputs: outputs}
}

// Limits on a failed step's excerpt, as sent to the server.
const (
	excerptLines    = 5
	excerptLineSize = 300
)

// outputTail keeps the last few non-empty lines a step wrote, per stream.
type outputTail struct {
	mu     sync.Mutex
	stdout []string
	stderr []string
}

func (t *outputTail) add(stream, text string) {
	text = strings.TrimRight(text, " \t\r")
	if strings.TrimSpace(text) == "" {
		return
	}
	if len(text) > excerptLineSize {
		cut := excerptLineSize
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	keep := func(lines []string) []string {
		lines = append(lines, text)
		if len(lines) > excerptLines {
			lines = lines[len(lines)-excerptLines:]
		}
		return lines
	}
	if stream == "stderr" {
		t.stderr = keep(t.stderr)
	} else {
		t.stdout = keep(t.stdout)
	}
}

// excerpt is the last lines on stderr, or on stdout if there were none.
func (t *outputTail) excerpt() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.stderr) > 0 {
		return strings.Join(t.stderr, "\n")
	}
	return strings.Join(t.stdout, "\n")
}

// resolveSecrets looks up the values of the job's secrets. If one cannot be
// had, it returns the result to report instead: failed when the secret does
// not exist or may not be read, lost (run it again) when the store is
// unavailable. Messages name the store and key, never a value.
func (r *Runner) resolveSecrets(ctx context.Context, job *engine.Job, sys func(string, ...any)) (map[string]string, *engine.Result) {
	values := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(job.Secrets)) {
		ref := job.Secrets[name]
		sys("Secret %s ← %s", name, ref)
		v, err := r.lookup(ctx, job.Project, ref, sys)
		if err == nil {
			values[name] = v
			continue
		}
		msg := fmt.Sprintf("secret %s (%s): %v", name, ref, err)
		switch {
		case ctx.Err() != nil:
			return nil, &engine.Result{Outcome: "canceled"}
		case secrets.IsUnavailable(err):
			sys("%s. The step will be run again.", msg)
			return nil, &engine.Result{Outcome: "lost", Error: msg}
		default:
			sys("%s", msg)
			return nil, &engine.Result{Outcome: "failed", Error: msg}
		}
	}
	return values, nil
}

// lookup asks for one secret, trying again with growing pauses for up to
// SecretWait while the store is unavailable, so that a short outage does not
// use up the step's tries.
func (r *Runner) lookup(ctx context.Context, project string, ref pipeline.SecretRef, sys func(string, ...any)) (string, error) {
	deadline := time.Now().Add(r.SecretWait)
	pause := time.Second
	for {
		v, err := r.Secrets.Lookup(ctx, project, ref)
		left := time.Until(deadline)
		if !secrets.IsUnavailable(err) || left <= 0 || ctx.Err() != nil {
			return v, err
		}
		pause = min(pause, left)
		sys("Secret store %s is unavailable (%v); asking again in %s", ref.Store, err, pause.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(pause):
		}
		pause *= 2
	}
}

// maskOutputs masks secret values in a step's outputs, which later steps
// receive and the server stores.
func maskOutputs(outputs map[string]string, mask *secrets.Masker) map[string]string {
	if mask == nil || outputs == nil {
		return outputs
	}
	masked := make(map[string]string, len(outputs))
	for k, v := range outputs {
		masked[mask.Mask(k)] = mask.Mask(v)
	}
	return masked
}

// workspaceID names the run's workspace on this runner. A server older
// than run affinity sends none; the run's ID then stands in.
func workspaceID(job *engine.Job) string {
	if job.Workspace != "" {
		return job.Workspace
	}
	return job.RunID
}

// workspace returns the directory a run's steps share on this runner,
// checking out the commit the first time. A workspace left from an earlier
// hold on the run (the run moved to another runner and back) is replaced.
func (r *Runner) workspace(ctx context.Context, job *engine.Job, sys func(string, ...any)) (string, error) {
	switch job.Source.Kind {
	case "dir":
		if len(job.Source.Libraries) > 0 {
			return "", errors.New("libraries need a run from a Git repository")
		}
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
	if id, err := os.ReadFile(ready); err == nil && string(id) == workspaceID(job) {
		sys("Using the run's workspace from an earlier step")
		return dir, nil
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	sys("Checking out %s at %s", redact.URLs(job.Source.URL), short(job.SHA))
	if err := git(ctx, "", "clone", "--quiet", "--no-checkout", job.Source.URL, dir); err != nil {
		return "", err
	}
	if err := git(ctx, dir, "checkout", "--quiet", "--detach", job.SHA); err != nil {
		return "", err
	}
	if err := r.addLibraries(ctx, job, dir, sys); err != nil {
		return "", err
	}
	// Containers may run as another user; let them write to the workspace.
	_ = os.Chmod(dir, 0o777)
	if err := os.WriteFile(ready, []byte(workspaceID(job)), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// collectGarbage removes workspaces nobody has touched for a day, and lets
// the executor release anything it holds for finished runs.
func (r *Runner) collectGarbage(ctx context.Context) {
	if cl, ok := r.Exec.(executor.Cleaner); ok {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for {
				if err := cl.Cleanup(ctx, r.Client.RunInfos); err != nil && ctx.Err() == nil {
					r.Log.Warn("cleaning up after finished runs", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}
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

func containerSpecs(job *engine.Job) []executor.ContainerSpec {
	out := make([]executor.ContainerSpec, len(job.Containers))
	for i, c := range job.Containers {
		out[i] = executor.ContainerSpec{Name: c.Name, Image: c.Image, Env: c.Env, CPU: c.CPU, Memory: c.Memory,
			Service: c.Service, Command: c.Command}
	}
	return out
}

func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s", args[0], redact.URLs(strings.TrimSpace(string(out))))
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
