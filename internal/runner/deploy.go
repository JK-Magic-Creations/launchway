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
	wg.Wait()
	lctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Client.Leave(lctx, d.Info.ID); err != nil {
		d.Log.Warn("could not tell the server this runner is leaving", "err", err)
	}
	return nil
}

// Health serves /healthz and /readyz like a runner's.
func (d *DeployRunner) Health() http.Handler {
	return (&Runner{Client: d.Client}).Health()
}

func (d *DeployRunner) actions(ctx context.Context) {
	slots := make(chan struct{}, d.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	backoff := time.Second
	wait := time.Duration(0)
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		job, err := d.Client.LeaseDeploy(ctx, d.Info, wait)
		wait = 25 * time.Second
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

// act does one action. The action is not interrupted when the run is
// canceled or the runner stops: it is short, and stopping it half-way
// (between a push and a sync) helps nobody. Its retries are safe.
func (d *DeployRunner) act(parent context.Context, job *engine.Job) {
	log := d.Log.With("run", job.RunID, "step", job.StepID, "action", job.Deploy.Action, "deployment", job.Deploy.Deployment)
	report, stop := context.WithTimeout(context.Background(), d.ActionTimeout+time.Minute)
	defer stop()
	if why := refusal(d.Accept, job, "deploy", true); why != "" {
		log.Warn("refusing a deploy action", "project", job.Project, "why", why)
		if err := refuse(report, d.Client, job, why); err != nil {
			log.Warn("could not report the refusal", "err", err)
		}
		return
	}
	if _, err := d.Client.Start(report, job.LeaseID); err != nil {
		log.Warn("could not start the action", "err", err)
		return
	}
	logs := newLogBuffer()
	sys := func(format string, args ...any) { logs.add("system", fmt.Sprintf(format, args...)) }
	var lostMu sync.Mutex
	lost := false
	flush := func() {
		for {
			batch := logs.take(engine.MaxLogBatch)
			if len(batch) == 0 {
				return
			}
			if err := d.Client.Logs(report, job.LeaseID, batch); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					lostMu.Lock()
					lost = true
					lostMu.Unlock()
				}
				return
			}
		}
	}
	bgStop, bgDone := make(chan struct{}), make(chan struct{})
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
			case <-hb.C:
				if _, err := d.Client.Heartbeat(report, job.LeaseID); errors.Is(err, ErrLeaseLost) {
					lostMu.Lock()
					lost = true
					lostMu.Unlock()
				}
			}
		}
	}()
	dj := job.Deploy
	sys("Deploy runner %s, target %s, application %s", d.Info.Name, dj.Target, dj.Application)
	if job.Attempt > 1 {
		sys("Attempt %d", job.Attempt)
	}
	// An action normally takes seconds; one held by a sync window may wait
	// until the step's deadline.
	until := time.Now().Add(d.ActionTimeout)
	if dl := time.UnixMilli(dj.Deadline); dj.Deadline > 0 && dl.After(until) {
		until = dl
	}
	actx, cancel := context.WithDeadline(context.Background(), until)
	var facts *engine.DeployFacts
	var err error
	// The action is safe to do again, so when something it needs cannot be
	// reached (the cluster, a secret store), it is tried again here, with
	// growing pauses, until the action's time runs out: a short outage does
	// not use up the step's tries.
	for pause := 2 * time.Second; ; pause = min(2*pause, 30*time.Second) {
		switch dj.Action {
		case engine.ActionApply:
			sys("Deploying %s %s to %s: committing to %s on %s", dj.Service, dj.Version, dj.Environment, dj.Repo, dj.Branch)
			facts, err = d.Agent.Apply(actx, job, sys)
		case engine.ActionRevert:
			sys("Rolling back %s in %s: reverting deployment %s on %s", dj.Service, dj.Environment, dj.Deployment, dj.Branch)
			facts, err = d.Agent.Revert(actx, job, sys)
		default:
			err = fmt.Errorf("this deploy runner does not know the action %q; it may be too old", dj.Action)
		}
		lostMu.Lock()
		gone := lost
		lostMu.Unlock()
		if !errors.Is(err, argocd.ErrUnavailable) || gone || parent.Err() != nil {
			break
		}
		if dl, _ := actx.Deadline(); time.Until(dl) < pause {
			break
		}
		sys("%v; trying again in %s", err, pause)
		select {
		case <-time.After(pause):
		case <-actx.Done():
		}
	}
	cancel()
	res := engine.Result{Outcome: "succeeded", Deploy: facts}
	switch {
	case err == nil:
		if facts != nil && facts.Sync != "" {
			sys("Sync: %s", facts.Sync)
		}
		sys("Done; waiting for Argo CD")
	case errors.Is(err, argocd.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded):
		sys("%v. The action will be tried again.", err)
		res = engine.Result{Outcome: "lost", Error: err.Error()}
	default:
		sys("%v", err)
		res = engine.Result{Outcome: "failed", Error: err.Error(), Deploy: facts}
	}
	close(bgStop)
	<-bgDone
	flush()
	lostMu.Lock()
	wasLost := lost
	lostMu.Unlock()
	if wasLost {
		log.Warn("the server gave the action away while it ran; its retry is safe")
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

// observe keeps one watcher per deployment the server says to watch.
func (d *DeployRunner) observe(ctx context.Context) {
	type watcher struct {
		cancel context.CancelFunc
		done   chan struct{}
	}
	watching := map[string]*watcher{}
	defer func() {
		for _, w := range watching {
			w.cancel()
			<-w.done
		}
	}()
	known := ""
	backoff := time.Second
	for ctx.Err() == nil {
		list, err := d.Client.Watch(ctx, d.Info, known, 20*time.Second)
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
				w.cancel()
				<-w.done
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
	backoff := time.Second
	for ctx.Err() == nil {
		job, err := d.Client.TakeCommand(ctx, d.Info, 25*time.Second)
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
