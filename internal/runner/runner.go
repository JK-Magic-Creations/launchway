package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/gitexec"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
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
	// Reports limits the report files collected after a step; zero fields
	// take the defaults (see executor.CollectLimits).
	Reports executor.CollectLimits
	// Images, if it names builders, lets the runner take image steps.
	Images *imagebuild.Config
	// AI, if set, lets the runner take ai steps: it calls the models the
	// server's registry names, with keys from its own secret stores.
	AI *ai.Client
	// Accept is the runner's own limits on the work it takes (its accept:
	// section). Every job is checked against it before anything runs.
	Accept *accept.Config
	// Remote is set for a runner on another machine than the server
	// (launchway runner): it never uses a directory of its own host as a
	// run's source, whatever the server says.
	Remote bool
	// CachePlatform is the platform build caches are kept for: the runner's
	// operating system and architecture (linux/amd64) unless a test says
	// otherwise.
	CachePlatform string
	// SpoolMaxBytes and SpoolMaxAge bound the log lines a step keeps on the
	// runner's disk while the server cannot be reached; zero means the
	// defaults (see spool.go).
	SpoolMaxBytes int64
	SpoolMaxAge   time.Duration

	spoolActive activeSteps   // steps this process is running
	spoolKick   chan struct{} // asks the spool delivery to look now
	wsMu        sync.Mutex
	wsLocks     map[string]*sync.Mutex
	reapKick    chan struct{} // asks the reaper to look now (see reapSoon)
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
	r.reapKick = make(chan struct{}, 1)
	r.spoolKick = make(chan struct{}, 1)
	if r.Info.Accept == nil && !r.Accept.Empty() {
		r.Info.Accept = r.Accept // the server leaves it out of work it refuses
	}
	if !r.Info.Has(engine.FeatureArtifacts) {
		r.Info.Features = strings.TrimSpace(r.Info.Features + " " + engine.FeatureArtifacts)
	}
	if !r.Info.Has(engine.FeatureCoverage) {
		r.Info.Features = strings.TrimSpace(r.Info.Features + " " + engine.FeatureCoverage)
	}
	if !r.Info.Has(engine.FeatureCache) {
		r.Info.Features = strings.TrimSpace(r.Info.Features + " " + engine.FeatureCache)
	}
	if r.Images != nil && len(r.Images.Builders) > 0 && !r.Info.Has(engine.FeatureImages) {
		r.Info.Features = strings.TrimSpace(r.Info.Features + " " + engine.FeatureImages)
	}
	if r.AI != nil && !r.Info.Has(engine.FeatureAI) {
		r.Info.Features = strings.TrimSpace(r.Info.Features + " " + engine.FeatureAI)
	}
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
	go r.deliverSpools(ctx)

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

// logBuffer batches lines for the next flush. Its size is bounded: when the
// server cannot keep up with a step that prints very fast, the step waits a
// little at the pipe (backpressure) instead of the runner holding without
// limit what it has not delivered. The wait is short and then the line is
// kept anyway, so a step is slowed, never stopped, and no line is lost.
type logBuffer struct {
	mu    sync.Mutex
	cond  *sync.Cond
	lines []engine.LogLine
	wake  chan struct{} // a batch worth sending is ready
}

const (
	// logBufferMax is how many undelivered lines a step may hold before its
	// output waits for a flush.
	logBufferMax = 50_000
	// logBufferWait is the longest such a wait lasts.
	logBufferWait = 2 * time.Second
	// logFlushNow is how many waiting lines send the batch without waiting
	// for the next tick.
	logFlushNow = 500
	// logFlushEvery is how often a step's waiting lines are sent: a line
	// reaches the server within about this time.
	logFlushEvery = 150 * time.Millisecond
)

func newLogBuffer() *logBuffer {
	b := &logBuffer{wake: make(chan struct{}, 1)}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *logBuffer) add(stream, text string) {
	b.mu.Lock()
	if len(b.lines) >= logBufferMax {
		// Wait for a flush, but not for long (and not forever if none runs).
		t := time.AfterFunc(logBufferWait, func() { b.mu.Lock(); b.cond.Broadcast(); b.mu.Unlock() })
		start := time.Now()
		for len(b.lines) >= logBufferMax && time.Since(start) < logBufferWait {
			b.cond.Wait()
		}
		t.Stop()
	}
	b.lines = append(b.lines, engine.LogLine{At: time.Now().UnixMilli(), Stream: stream, Text: text})
	n := len(b.lines)
	b.mu.Unlock()
	if n >= logFlushNow {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
}

func (b *logBuffer) take(max int) []engine.LogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(b.lines), max)
	out := append([]engine.LogLine(nil), b.lines[:n]...)
	b.lines = b.lines[n:]
	if len(b.lines) == 0 {
		b.lines = nil
	}
	b.cond.Broadcast()
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

	logs := newLogBuffer()
	sp := r.newSpool(job)
	r.spoolActive.add(job.LeaseID)
	defer r.spoolActive.done(job.LeaseID)
	sys := func(format string, args ...any) { logs.add("system", fmt.Sprintf(format, args...)) }
	flush := func() {
		for {
			batch := logs.take(engine.MaxLogBatch)
			if len(batch) == 0 {
				if err := sp.Drain(report); err != nil && !errors.Is(err, ErrLeaseLost) {
					log.Warn("delivering kept logs", "err", err)
				} else if err != nil {
					stopWith(false, true)
				}
				return
			}
			if err := sp.Send(report, batch); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					stopWith(false, true)
				} else {
					log.Warn("sending logs", "err", err)
				}
				return
			}
		}
	}

	exName := r.Exec.Name()
	if job.ImageBuild != nil {
		exName = "buildkit"
	}
	if why := refusal(r.Accept, job, exName, r.Remote); why != "" {
		log.Warn("refusing a step", "project", job.Project, "why", why)
		if err := refuse(report, r.Client, job, why); err != nil {
			log.Warn("could not report the refusal", "err", err)
		}
		return
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
		fl := time.NewTicker(logFlushEvery)
		defer hb.Stop()
		defer fl.Stop()
		for {
			select {
			case <-bgStop:
				return
			case <-fl.C:
				flush()
			case <-logs.wake:
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

	res, collect := r.runStep(ctx, job, sys, logs.add)

	mu.Lock()
	wasRequested, wasLost := requested, lost
	mu.Unlock()
	// Report files are collected whether the step passed or failed, was
	// canceled or timed out; not when the server has moved on, nor when the
	// runner is shutting down (the step will run again elsewhere).
	if collect != nil && !wasLost && parent.Err() == nil {
		got := collect(outcomeOf(res, wasRequested))
		res.Tests, res.Findings, res.Coverage, res.Caches = got.tests, got.findings, got.coverage, got.caches
	}
	close(bgStop)
	<-bgDone
	mu.Lock()
	wasRequested, wasLost = requested, lost
	mu.Unlock()
	switch {
	case wasLost:
		res = engine.Result{} // the server already moved on; nothing to report
	case wasRequested:
		res = engine.Result{Outcome: "canceled", Error: "canceled", Tests: res.Tests, Findings: res.Findings, Coverage: res.Coverage, Caches: res.Caches}
	case parent.Err() != nil:
		res = engine.Result{Outcome: "canceled", Error: "the runner is shutting down"}
		sys("The runner is shutting down; the step will be run again.")
	}
	flush()
	if wasLost {
		return
	}
	if !sp.Done() {
		// Lines are still waiting for the server: the result goes after them.
		r.keepResult(log, sp, res)
		return
	}
	for i := 0; i < 5; i++ {
		err := r.reportResult(report, log, job.LeaseID, &res)
		if err == nil || errors.Is(err, ErrLeaseLost) {
			r.finished(log, res)
			return
		}
		if isOutage(err) {
			// The step's outcome is kept and delivered when the server
			// answers; it is not given up after a few tries.
			r.keepResult(log, sp, res)
			return
		}
		log.Warn("reporting the result failed; retrying", "err", err)
		time.Sleep(time.Duration(i+1) * time.Second)
	}
}

// keepResult spools a step's result and wakes the delivery.
func (r *Runner) keepResult(log *slog.Logger, sp *stepSpool, res engine.Result) {
	sp.markOutage()
	if err := sp.SaveResult(res); err != nil {
		log.Error("could not keep the result on disk; it is lost if the runner stops", "err", err)
		return
	}
	log.Warn("the server could not be reached; the result is kept and will be delivered when it answers")
	select {
	case r.spoolKick <- struct{}{}:
	default:
	}
}

// reportResult sends a step's result. A server that refuses report files as
// too large gets the result without them: the outcome matters more.
func (r *Runner) reportResult(ctx context.Context, log *slog.Logger, lease string, res *engine.Result) error {
	for {
		err := r.Client.Finish(ctx, lease, *res)
		switch {
		case errors.Is(err, ErrTooLarge) && res.Findings != nil:
			log.Warn("the server refused the step's SARIF files as too large; reporting the result without them", "err", err)
			res.Findings = &engine.CollectedReports{Files: []engine.ReportFile{},
				Notes: append(res.Findings.Notes, "the server refused the SARIF files as too large, so none were kept")}
		case errors.Is(err, ErrTooLarge) && res.Coverage != nil && len(res.Coverage.Files) > 0:
			log.Warn("the server refused the step's coverage reports as too large; reporting the result without them", "err", err)
			res.Coverage = &engine.CollectedReports{Files: []engine.ReportFile{},
				Notes: append(res.Coverage.Notes, "the server refused the coverage reports as too large, so none were kept")}
		case errors.Is(err, ErrTooLarge) && res.Tests != nil:
			log.Warn("the server refused the step's report files as too large; reporting the result without them", "err", err)
			res.Tests = &engine.CollectedReports{Files: []engine.ReportFile{},
				Notes: append(res.Tests.Notes, "the server refused the report files as too large, so none were kept")}
		default:
			return err
		}
	}
}

// collectTimeout bounds how long collecting a step's report files may take.
const collectTimeout = 2 * time.Minute

// collector returns what collects a step's report files once it has run,
// or nil if the step declares none.
func (r *Runner) collector(job *engine.Job, spec executor.Spec, sys func(string, ...any), caches *cacheRun) func(outcome string) collected {
	tests := r.testCollector(job, spec, sys)
	findings := r.findingCollector(job, spec, sys)
	cover := r.coverageCollector(job, spec, sys)
	if tests == nil && findings == nil && cover == nil && job.Artifacts == nil && caches == nil {
		return nil
	}
	return func(outcome string) collected {
		var c collected
		if tests != nil {
			c.tests = tests()
		}
		if findings != nil {
			c.findings = findings()
		}
		if cover != nil {
			c.coverage = cover()
		}
		r.keepArtifacts(job, spec, outcome, sys)
		if caches != nil {
			caches.save(outcome)
			c.caches = caches.results()
		}
		return c
	}
}

// testCollector returns what collects a step's test report files, or nil.
func (r *Runner) testCollector(job *engine.Job, spec executor.Spec, sys func(string, ...any)) func() *engine.CollectedReports {
	if job.Reports == nil || len(job.Reports.Tests) == 0 {
		return nil
	}
	return func() *engine.CollectedReports {
		lim := r.Reports.WithDefaults()
		if job.Reports.MaxBytes > 0 {
			// The server says how much it accepts; JSON carries the bytes
			// as base64, so a little room is left for that and the rest.
			lim.MaxBytes = min(lim.MaxBytes, job.Reports.MaxBytes)
			lim.MaxFileBytes = min(lim.MaxFileBytes, lim.MaxBytes)
		}
		ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
		defer cancel()
		got, err := executor.Collect(ctx, r.execFor(job), spec, job.Reports.Tests, lim)
		out := &engine.CollectedReports{Files: []engine.ReportFile{}, Workspace: r.execFor(job).WorkspacePath(spec.Workspace)}
		if got != nil {
			for _, f := range got.Files {
				out.Files = append(out.Files, engine.ReportFile{Path: f.Path, Data: f.Data, Size: f.Size, Cut: f.Cut})
			}
			out.Notes = got.Notes
		}
		if err != nil {
			msg := "Could not collect the test reports: " + spec.Mask(err.Error())
			if errors.Is(err, context.DeadlineExceeded) {
				msg = fmt.Sprintf("Collecting the test reports took longer than %s; stopped", collectTimeout)
			}
			out.Notes = append(out.Notes, msg)
		}
		var size int64
		for _, f := range out.Files {
			size += int64(len(f.Data))
		}
		switch n := len(out.Files); {
		case n == 1:
			sys("Test reports: collected %s (%s)", out.Files[0].Path, sizeText(size))
		case n > 1:
			sys("Test reports: collected %d files (%s)", n, sizeText(size))
		}
		for _, note := range out.Notes {
			sys("Test reports: %s", note)
		}
		return out
	}
}

func sizeText(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1024), ".0") + " KiB"
	default:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/(1<<20)), ".0") + " MiB"
	}
}

// runStep runs one attempt. Once the step has been handed to the executor,
// it also returns what collects its report files, if it declares any.
func (r *Runner) runStep(ctx context.Context, job *engine.Job, sys func(string, ...any), out executor.Sink) (engine.Result, func(string) collected) {
	if job.AI != nil {
		return r.runAI(ctx, job, sys, out)
	}
	if job.ImageBuild != nil {
		return r.runImage(ctx, job, sys, out)
	}
	sys("Runner %s, executor %s", r.Info.Name, r.Exec.Name())
	if job.TakenOverFrom != "" {
		sys("This run moves here from runner %s, which stopped responding or left. It continues in a fresh workspace: files that earlier steps wrote are not in it.", job.TakenOverFrom)
	}
	if job.Attempt > 1 {
		sys("Attempt %d", job.Attempt)
	}
	values, failed := r.resolveSecrets(ctx, job, sys)
	if failed != nil {
		return *failed, nil
	}
	mask, short := secrets.NewMasker(values)
	for _, name := range short {
		sys("%s has a value, or a line of one, shorter than %d characters; it is not masked in the output", name, secrets.MinMaskLen)
	}
	env := make(map[string]string, len(job.Env)+len(values)+1)
	// A step's output is not a terminal, so Python would hold it back in a
	// block buffer and a person would see nothing for minutes. The step's
	// own environment may set it back.
	env["PYTHONUNBUFFERED"] = "1"
	maps.Copy(env, job.Env)
	maps.Copy(env, values)

	ws, err := r.workspace(ctx, job, sys)
	if err != nil {
		sys("Could not prepare the workspace: %v", err)
		return engine.Result{Outcome: "failed", Cause: pipeline.CauseInfrastructure, Error: "preparing the workspace: " + err.Error()}, nil
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
	spec := executor.Spec{
		RunID: job.RunID, Containers: containerSpecs(job), Container: job.Container,
		Name: "launchway-" + job.LeaseID, Image: job.Image, Script: job.Script, Env: env,
		Workspace: ws, WorkspaceID: workspaceID(job), MetaDir: meta, Mask: mask.Mask, Hold: mask.Hold, AsksResources: job.ResourcesAsked,
	}
	caches := r.newCacheRun(job, spec, sys)
	collect := r.collector(job, spec, sys, caches)
	if failed := r.fetch(ctx, job, spec, sys, out); failed != nil {
		return *failed, nil
	}
	if caches != nil {
		caches.restore(ctx, out)
	}
	code, err := r.Exec.Run(tctx, spec, func(stream, text string) {
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
		return engine.Result{Outcome: "failed", Cause: pipeline.CauseTimeout, Error: fmt.Sprintf("timed out after %s", job.Timeout.D()), Excerpt: tail.excerpt()}, collect
	case err != nil && ctx.Err() != nil:
		return engine.Result{Outcome: "canceled"}, collect
	case errors.Is(err, executor.ErrInfrastructure):
		msg := mask.Mask(err.Error())
		sys("%s. The step will be run again.", msg)
		return engine.Result{Outcome: "lost", Cause: pipeline.CauseInfrastructure, Error: msg}, nil
	case errors.Is(err, executor.ErrCapacity):
		// No room yet: not a failure. The server queues the step again after a
		// pause, and says on the run page what it waits for.
		msg := mask.Mask(err.Error())
		code := ""
		if c, ok := executor.AsCapacity(err); ok {
			code = c.Code
		}
		sys("%s. The step stays queued and is tried again.", msg)
		return engine.Result{Outcome: engine.OutcomeWaiting, Cause: pipeline.CauseInfrastructure, Error: msg, WaitCode: code}, nil
	case err != nil:
		msg := mask.Mask(err.Error())
		sys("Could not run the step: %s", msg)
		res := engine.Result{Outcome: "failed", Error: msg}
		if errors.Is(err, executor.ErrCannotStart) {
			res.Cause = pipeline.CauseInfrastructure
		}
		return res, collect
	}
	outputs := maskOutputs(executor.ReadOutputs(meta), mask)
	ec := code
	if code != 0 {
		why := fmt.Sprintf("exit code %d", code)
		if x, ok := r.Exec.(executor.ExitExplainer); ok {
			if note := x.ExplainExit(ctx, spec, code); note != "" {
				why = note
				sys("%s", note)
			}
		}
		sys("Failed with exit code %d after %s", code, took)
		return engine.Result{Outcome: "failed", Cause: pipeline.CauseExit, ExitCode: &ec, Error: why, Outputs: outputs,
			Excerpt: tail.excerpt()}, collect
	}
	sys("Succeeded in %s", took)
	return engine.Result{Outcome: "succeeded", ExitCode: &ec, Outputs: outputs}, collect
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
			return nil, &engine.Result{Outcome: "lost", Cause: pipeline.CauseInfrastructure, Error: msg}
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
		go r.reap(ctx, cl, r.reapKick, reapEvery)
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

// reapEvery is how often the reaper asks the server which runs have ended.
const reapEvery = 30 * time.Second

// reap is the reaper: it has the executor release what it holds for runs
// that ended (a Kubernetes run's pod, which holds namespace quota), every
// interval, at once at the start (a crashed runner's pods are found by the
// next one), and soon after any step reports its result, which may be the
// run's last. The executor decides what to release (see executor.Cleaner).
func (r *Runner) reap(ctx context.Context, cl executor.Cleaner, kick <-chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := cl.Cleanup(ctx, r.Client.RunInfos); err != nil && ctx.Err() == nil {
			r.Log.Warn("cleaning up after finished runs", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
			// Let the server settle the run's status first.
			select {
			case <-ctx.Done():
				return
			case <-time.After(min(2*time.Second, every)):
			}
		}
	}
}

// finished logs a step's result and has the reaper look, since it may have
// been the run's last.
func (r *Runner) finished(log *slog.Logger, res engine.Result) {
	log.Info("step finished", "outcome", res.Outcome)
	r.reapSoon()
}

// reapSoon asks the reaper to look now; it never waits.
func (r *Runner) reapSoon() {
	select {
	case r.reapKick <- struct{}{}:
	default:
	}
}

func containerSpecs(job *engine.Job) []executor.ContainerSpec {
	out := make([]executor.ContainerSpec, len(job.Containers))
	for i, c := range job.Containers {
		out[i] = executor.ContainerSpec{Name: c.Name, Image: c.Image, Env: c.Env, CPU: c.CPU, Memory: c.Memory,
			Service: c.Service, Command: c.Command,
			CPULimit: c.CPULimit, MemoryLimit: c.MemoryLimit, Threads: c.Threads,
			ExplicitCPU: c.ExplicitCPU, ExplicitMemory: c.ExplicitMemory, Ready: readySpec(c.Ready)}
	}
	return out
}

// git runs git through the one hardened runner. The repositories a runner
// clones belong to other people, so their objects are checked on transfer.
func git(ctx context.Context, dir string, args ...string) error {
	_, err := gitexec.Run(ctx, gitexec.Spec{Dir: dir, Args: args, Inherit: gitexec.Remote, Untrusted: true})
	if err != nil {
		var ee *gitexec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("git %s: %s", args[0], redact.URLs(ee.Output()))
		}
		return fmt.Errorf("git %s: %s", args[0], redact.URLs(err.Error()))
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
