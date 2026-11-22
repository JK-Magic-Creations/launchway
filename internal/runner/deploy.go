package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/accept"
	"github.com/jk-magic-creations/launchway/internal/ai"
	"github.com/jk-magic-creations/launchway/internal/deploy/argocd"
	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/metrics"
	"github.com/jk-magic-creations/launchway/internal/secrets"
	hs "github.com/jk-magic-creations/launchway/internal/verify/source"
)

// DeployRunner does the work of deploy and rollback steps for the targets
// its configuration serves: it takes their actions (commit and sync, revert
// and sync) and observes the deployments being watched, reporting what it
// sees. It needs the token of a runner pool that may serve those targets.
// It runs no scripts and needs no workspace.
type DeployRunner struct {
	Client *Client
	Agent  *argocd.Agent
	Info   engine.RunnerInfo // Targets: the targets it serves
	// Concurrency is how many actions it does at once; default 2.
	Concurrency int
	Log         *slog.Logger
	// ObserveEvery is how often it reads each watched deployment even when
	// no change was announced; default 5s. ReportEvery is how often it
	// reports a snapshot that has not changed, so the server knows it is
	// still looking; default 10s.
	ObserveEvery, ReportEvery time.Duration
	// ActionTimeout bounds one action; default 10m.
	ActionTimeout time.Duration
	// IdleEvery is how often it reads an idle service (one nothing is
	// deploying), reporting it at least every other time; default 30s.
	IdleEvery time.Duration
	// Sources, if set, are the health sources the runner reads for verify
	// steps (schema 19); it then takes verifications of its targets,
	// VerifyConcurrency at a time (default 8). AI calls models to explain
	// verdicts, with keys from Secrets.
	Sources           *hs.Sources
	VerifyConcurrency int
	// Settle is how long after an interval ends its data are read; default
	// 10s.
	Settle  time.Duration
	AI      *ai.Client
	Secrets *secrets.Resolver
	// Accept is the runner's own limits (its accept: section): projects,
	// environments, repositories and whether pull-request runs may deploy.
	Accept *accept.Config
	// ActionGrace is how long an action that was told to stop (its time ran out,
	// the run was canceled, the server ended the step) may take to do so before
	// the runner gives it up and frees its place; default 30s.
	ActionGrace time.Duration
	// StallAfter is how long a loop that takes work from the server may go
	// without a poll before the runner is judged stuck: /livez then answers 503
	// and the watchdog ends the process (Run returns ErrStalled); default 5m.
	StallAfter time.Duration
	// StatusEvery is how often the runner logs one line of what it has done
	// (polls made, when work was last taken); default 10m, negative: never.
	StatusEvery time.Duration

	vitOnce sync.Once
	vit     *vitals
}

// vitals is the runner's pulse, made on first use (the health listener may ask
// before Run has started).
func (d *DeployRunner) vitals() *vitals {
	d.vitOnce.Do(func() { d.vit = newVitals(d.StallAfter) })
	return d.vit
}

// Run takes actions and observes until ctx ends.
func (d *DeployRunner) Run(ctx context.Context) error {
	if d.Concurrency <= 0 {
		d.Concurrency = 2
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.ObserveEvery <= 0 {
		d.ObserveEvery = 5 * time.Second
	}
	if d.ReportEvery <= 0 {
		d.ReportEvery = 10 * time.Second
	}
	if d.ActionTimeout <= 0 {
		d.ActionTimeout = 10 * time.Minute
	}
	if d.Info.Accept == nil && !d.Accept.Empty() {
		d.Info.Accept = d.Accept // the server leaves verifications it would refuse to other runners
	}
	if !d.Info.Has(engine.FeatureIdle) {
		d.Info.Features = strings.TrimSpace(d.Info.Features + " " + engine.FeatureIdle)
	}
	if d.IdleEvery <= 0 {
		d.IdleEvery = 30 * time.Second
	}
	if d.ActionGrace <= 0 {
		d.ActionGrace = 30 * time.Second
	}
	if d.StatusEvery == 0 {
		d.StatusEvery = 10 * time.Minute
	}
	d.vitals()
	if d.Agent != nil && d.Agent.Pulls == nil {
		d.Agent.Pulls = d.Client
	}
	d.Log.Info("deploy runner started", "name", d.Info.Name, "targets", d.Info.Targets, "server", d.Client.Server)
	var wg sync.WaitGroup
	if d.Sources != nil {
		if !d.Info.Has(FeatureVerify) {
			d.Info.Features = strings.TrimSpace(d.Info.Features + " " + FeatureVerify)
		}
		wg.Add(1)
		go func() { defer wg.Done(); d.verifications(ctx) }()
	}
	wg.Add(4)
	go func() { defer wg.Done(); d.actions(ctx) }()
	go func() { defer wg.Done(); d.observe(ctx) }()
	go func() { defer wg.Done(); d.commands(ctx) }()
	go func() { defer wg.Done(); d.inspections(ctx) }()
	stalled := make(chan error, 1)
	gctx, stopGuard := context.WithCancel(ctx)
	defer stopGuard()
	d.vitals().publish(metrics.Default)
	go d.vitals().guard(gctx, d.Log, stalled)
	go d.vitals().report(gctx, d.Log, d.StatusEvery, "deploy runner")
	allDone := make(chan struct{})
	go func() { wg.Wait(); close(allDone) }()
	select {
	case <-allDone:
	case err := <-stalled:
		// Something holds a loop for good: waiting for it would wait for ever.
		return err
	}
	lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Client.Leave(lctx, d.Info.ID); err != nil {
		d.Log.Warn("could not tell the server this runner is leaving", "err", err)
	}
	return nil
}

// Health serves /healthz and /readyz like a runner's, and /livez, which answers
// 200 while every loop that takes work polls the server.
func (d *DeployRunner) Health() http.Handler {
	base := (&Runner{Client: d.Client}).Health()
	mux := http.NewServeMux()
	// /livez is for a liveness probe: it fails when a loop that takes work has
	// stopped polling, which /healthz (the process is up) cannot say.
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { d.vitals().liveHandler()(w, r) })
	mux.Handle("/", base)
	return mux
}

func (d *DeployRunner) actions(ctx context.Context) {
	vl := d.vitals().loop("deploy lease")
	slots := make(chan struct{}, d.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	backoff := time.Second
	wait := time.Duration(0)
	for {
		vl.slotWait(true)
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		vl.slotWait(false)
		job, err := d.Client.LeaseDeploy(ctx, d.Info, wait)
		wait = 25 * time.Second
		vl.polled()
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return
			}
			d.Log.Warn("could not take deploy work from the server; retrying", "err", err, "in", backoff)
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
			d.act(ctx, job)
		}()
	}
}

// actionResult is what a deploy action returned.
type actionResult struct {
	facts *engine.DeployFacts
	err   error
}

// doAction does one action on ctx and waits for it for as long as ctx lives and
// then a grace, so that an action that does not look at its context (a call
// that cannot be interrupted) cannot hold its place for ever: when the grace is
// over the runner gives it up, says so, and reports the action as lost, which
// the server tries again. What the action was doing may still be running; it
// holds nothing the runner waits for without a deadline.
func (d *DeployRunner) doAction(ctx context.Context, job *engine.Job, sys func(string, ...any)) actionResult {
	dj := job.Deploy
	done := make(chan actionResult, 1)
	go func() {
		var r actionResult
		switch dj.Action {
		case engine.ActionApply:
			sys("Deploying %s %s to %s: committing to %s on %s", dj.Service, dj.Version, dj.Environment, dj.Repo, dj.Branch)
			r.facts, r.err = d.Agent.Apply(ctx, job, sys)
		case engine.ActionRevert:
			sys("Rolling back %s in %s: reverting deployment %s on %s", dj.Service, dj.Environment, dj.Deployment, dj.Branch)
			r.facts, r.err = d.Agent.Revert(ctx, job, sys)
		default:
			r.err = fmt.Errorf("this deploy runner does not know the action %q; it may be too old", dj.Action)
		}
		done <- r
	}()
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
	}
	t := time.NewTimer(d.ActionGrace)
	defer t.Stop()
	select {
	case r := <-done:
		return r
	case <-t.C:
		d.vitals().abandoned.Add(1)
		d.Log.Error("a deploy action did not stop when its time ran out; giving it up", "run", job.RunID, "step", job.StepID, "grace", d.ActionGrace)
		sys("The action did not stop %s after its time ran out; the deploy runner gave it up, and it will be tried again", d.ActionGrace)
		return actionResult{err: fmt.Errorf("the action did not stop when its time ran out: %w", context.DeadlineExceeded)}
	}
}

// actState is what the server told the runner about the step an action serves.
type actState struct {
	mu        sync.Mutex
	lost      bool // the server no longer has the lease: it ended the step or gave it away
	requested bool // the run was canceled
	cancel    context.CancelFunc
}

// end records what the server said and stops the action.
func (s *actState) end(canceled, gone bool) {
	s.mu.Lock()
	s.lost, s.requested = s.lost || gone, s.requested || canceled
	s.mu.Unlock()
	s.cancel()
}

func (s *actState) flags() (lost, requested bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lost, s.requested
}

// reportBack sends the action's log lines and heartbeats until the returned
// function is called, which sends what is left and waits for both to end.
func (d *DeployRunner) reportBack(report context.Context, job *engine.Job, log *slog.Logger, logs *logBuffer, st *actState, sys func(string, ...any)) (stop func()) {
	flush := func() {
		for {
			batch := logs.take(engine.MaxLogBatch)
			if len(batch) == 0 {
				return
			}
			if err := d.Client.Logs(report, job.LeaseID, batch); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					st.end(false, true)
				}
				return
			}
		}
	}
	bgStop, bgDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(bgDone)
		fl := time.NewTicker(logFlushEvery)
		defer fl.Stop()
		for {
			select {
			case <-bgStop:
				return
			case <-fl.C:
				flush()
			}
		}
	}()
	beatStop, beatDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(beatDone)
		keepLease(report, beatStop, d.Client, job, log, func(canceled, gone bool) {
			if canceled {
				sys("The run was canceled; stopping the action.")
			}
			st.end(canceled, gone)
		})
	}()
	return func() {
		close(beatStop)
		<-beatDone
		close(bgStop)
		<-bgDone
		flush()
	}
}

// doWithRetries does the action. It is safe to do again, so when something it
// needs cannot be reached (the cluster, a secret store), it is tried again with
// growing pauses until the action's time runs out: a short outage does not use
// up the step's tries.
func (d *DeployRunner) doWithRetries(parent, actx context.Context, job *engine.Job, st *actState, sys func(string, ...any)) actionResult {
	for pause := 2 * time.Second; ; pause = min(2*pause, 30*time.Second) {
		r := d.doAction(actx, job, sys)
		lost, requested := st.flags()
		if !errors.Is(r.err, argocd.ErrUnavailable) || lost || requested || parent.Err() != nil {
			return r
		}
		if dl, _ := actx.Deadline(); time.Until(dl) < pause {
			return r
		}
		sys("%v; trying again in %s", r.err, pause)
		select {
		case <-time.After(pause):
		case <-actx.Done():
		}
	}
}

// actionOutcome is the result to report for an action that ended with err.
func actionOutcome(r actionResult, requested bool, sys func(string, ...any)) engine.Result {
	switch {
	case requested:
		sys("The action was stopped because the run was canceled.")
		return engine.Result{Outcome: "canceled", Error: "canceled", Deploy: r.facts}
	case r.err == nil:
		if r.facts != nil && r.facts.Sync != "" {
			sys("Sync: %s", r.facts.Sync)
		}
		sys("Done; waiting for Argo CD")
		return engine.Result{Outcome: "succeeded", Deploy: r.facts}
	case errors.Is(r.err, argocd.ErrUnavailable) || errors.Is(r.err, context.DeadlineExceeded):
		sys("%v. The action will be tried again.", r.err)
		return engine.Result{Outcome: "lost", Error: r.err.Error()}
	}
	sys("%v", r.err)
	return engine.Result{Outcome: "failed", Error: r.err.Error(), Deploy: r.facts}
}

// act does one action. The server tells the runner when the step it holds was
// ended (the lease is gone) or the run was canceled: the action then stops at
// once, and its retries are safe.
func (d *DeployRunner) act(parent context.Context, job *engine.Job) {
	log := d.Log.With("run", job.RunID, "step", job.StepID, "action", job.Deploy.Action, "deployment", job.Deploy.Deployment)
	// An action normally takes seconds; one held by a sync window may wait
	// until the step's deadline.
	dj := job.Deploy
	until := time.Now().Add(d.ActionTimeout)
	if dl := time.UnixMilli(dj.Deadline); dj.Deadline > 0 && dl.After(until) {
		until = dl
	}
	// The calls to the server (heartbeats, logs, the result) go on for as long
	// as the action may, and a little after.
	report, stop := context.WithDeadline(context.Background(), until.Add(d.ActionGrace+time.Minute))
	defer stop()
	if why := refusal(d.Accept, job, "deploy", true); why != "" {
		log.Warn("refusing a deploy action", "project", job.Project, "why", why)
		if err := refuse(report, d.Client, job, why); err != nil {
			log.Warn("could not report the refusal", "err", err)
		}
		return
	}
	defer d.vitals().begin(fmt.Sprintf("deploy %s %s", dj.Action, dj.Service), until.Add(d.ActionGrace))()
	actx, cancelAct := context.WithDeadline(context.Background(), until)
	defer cancelAct()
	if _, err := d.Client.Start(report, job.LeaseID); err != nil {
		log.Warn("could not start the action", "err", err)
		return
	}
	logs := newLogBuffer()
	sys := func(format string, args ...any) { logs.add("system", fmt.Sprintf(format, args...)) }
	st := &actState{cancel: cancelAct}
	stopReporting := d.reportBack(report, job, log, logs, st, sys)
	sys("Deploy runner %s, target %s, application %s", d.Info.Name, dj.Target, dj.Application)
	if job.Attempt > 1 {
		sys("Attempt %d", job.Attempt)
	}
	r := d.doWithRetries(parent, actx, job, st, sys)
	cancelAct()
	_, requested := st.flags()
	res := actionOutcome(r, requested, sys)
	stopReporting()
	if lost, _ := st.flags(); lost {
		log.Warn("the server ended or gave away the step while the action ran; the action was stopped, and its retry is safe")
		return
	}
	for i := 0; i < 5; i++ {
		err := d.Client.Finish(report, job.LeaseID, res)
		if err == nil || errors.Is(err, ErrLeaseLost) {
			log.Info("deploy action finished", "outcome", res.Outcome)
			return
		}
		log.Warn("reporting the action's result failed; retrying", "err", err)
		time.Sleep(time.Duration(i+1) * time.Second)
	}
}

// watchStopWithin is how long the loop waits for a watch it stopped to end.
const watchStopWithin = 10 * time.Second

// observe keeps one watcher per deployment the server says to watch.
func (d *DeployRunner) observe(ctx context.Context) {
	type watcher struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	vl := d.vitals().loop("observe")
	watching := map[string]*watcher{}
	// stopWatcher ends one watch and waits for it, but not for ever: a watch
	// stuck in something that does not look at its context is given up (it holds
	// nothing this loop waits for), so that the list of what to observe is still
	// followed, and the steps that come after it are still seen.
	stopWatcher := func(key string, w *watcher) {
		w.cancel()
		t := time.NewTimer(watchStopWithin)
		defer t.Stop()
		select {
		case <-w.done:
		case <-t.C:
			d.Log.Error("an observation did not stop when the server stopped listing it; giving it up", "watch", key, "within", watchStopWithin)
		}
	}
	defer func() {
		for key, w := range watching {
			stopWatcher(key, w)
		}
	}()
	known := ""
	backoff := time.Second
	for ctx.Err() == nil {
		vl.beat()
		list, err := d.Client.Watch(ctx, d.Info, known, 20*time.Second)
		vl.polled()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			d.Log.Warn("could not ask the server what to observe; retrying", "err", err, "in", backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if list == nil {
			continue
		}
		known = list.Version
		keep := map[string]bool{}
		for _, it := range list.Items {
			keep[it.Key] = true
			if watching[it.Key] != nil {
				continue
			}
			wctx, cancel := context.WithCancel(ctx)
			w := &watcher{cancel: cancel, done: make(chan struct{})}
			watching[it.Key] = w
			go func(it engine.WatchItem) {
				defer close(w.done)
				d.watch(wctx, it)
			}(it)
		}
		for key, w := range watching {
			if !keep[key] {
				stopWatcher(key, w)
				delete(watching, key)
			}
		}
	}
}

// watch observes one deployment until ctx ends (the server stops listing
// it): at once, whenever its objects change, and every ObserveEvery.
func (d *DeployRunner) watch(ctx context.Context, it engine.WatchItem) {
	log := d.Log.With("deployment", it.Deployment, "run", it.RunID, "step", it.Step)
	changed := make(chan struct{}, 1)
	every, report := d.ObserveEvery, d.ReportEvery
	if it.Kind == engine.WatchUnit {
		// Idle: no watches, and slowly; bounded by the server too.
		every, report = d.IdleEvery, 2*d.IdleEvery
	} else {
		d.Agent.Watch(ctx, it, changed)
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	var last []byte
	var lastSent time.Time
	for {
		octx, cancel := context.WithTimeout(ctx, 30*time.Second)
		snap := d.Agent.Observe(octx, it)
		cancel()
		if ctx.Err() != nil {
			return
		}
		data, _ := json.Marshal(snap)
		if !slices.Equal(data, last) || time.Since(lastSent) >= report {
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			res, err := d.Client.Observe(rctx, d.Info, []engine.Observation{{Key: it.Key, Snapshot: snap}})
			cancel()
			switch {
			case err != nil:
				log.Warn("could not report what was observed", "err", err)
			default:
				last, lastSent = data, time.Now()
				for _, r := range res {
					if !r.Accepted {
						log.Info("the server did not take an observation", "reason", r.Reason)
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
			// Let a burst of changes settle.
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				return
			}
		case <-tick.C:
		}
	}
}

// commands takes commands on rollouts (promote, abort and the like) and
// applies them, one at a time: they are rare, and each takes a second.
func (d *DeployRunner) commands(ctx context.Context) {
	vl := d.vitals().loop("commands")
	backoff := time.Second
	for ctx.Err() == nil {
		vl.beat()
		job, err := d.Client.TakeCommand(ctx, d.Info, 25*time.Second)
		vl.polled()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait := backoff
			if strings.Contains(err.Error(), "no such API endpoint") {
				wait = 5 * time.Minute // a server that does not know commands
			}
			d.Log.Warn("could not ask the server for commands; retrying", "err", err, "in", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		if job == nil {
			continue
		}
		d.command(ctx, job)
	}
}

// command applies one command and reports it. A command the cluster could
// not be reached for is handed back, and the runner pauses before taking
// another.
func (d *DeployRunner) command(ctx context.Context, job *engine.CommandJob) {
	log := d.Log.With("command", job.ID, "action", job.Action, "deployment", job.Deployment)
	until := time.UnixMilli(job.Until)
	if time.Until(until) < 5*time.Second {
		until = time.Now().Add(5 * time.Second)
	}
	actx, cancel := context.WithDeadline(context.Background(), until.Add(-2*time.Second))
	result, detail, err := d.Agent.Command(actx, job, func(f string, a ...any) { log.Info(fmt.Sprintf(f, a...)) })
	cancel()
	res := engine.CommandResult{Take: job.Take, Result: result, Detail: detail}
	pause := false
	if err != nil {
		res.Result, res.Detail = engine.CommandReleased, "the deploy runner could not reach the rollout: "+err.Error()
		pause = true
	}
	for i := 0; i < 5; i++ {
		rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := d.Client.FinishCommand(rctx, d.Info, res)
		cancel()
		if err == nil || errors.Is(err, ErrLeaseLost) {
			log.Info("command finished", "result", res.Result, "detail", res.Detail)
			break
		}
		log.Warn("reporting a command's result failed; retrying", "err", err)
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	if pause {
		select {
		case <-time.After(10 * time.Second):
		case <-ctx.Done():
		}
	}
}
