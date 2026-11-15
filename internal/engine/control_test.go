package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// gatedPipeline is the design's gated canary: deploy to the first pause,
// smoke the canary, approve, promote, roll back (aborting first) on any
// failure.
const gatedPipeline = `pipeline: shop
steps:
  build:
    image: x
    run: build
  deploy-prod:
    kind: deploy
    needs: build
    environment: prod
    version: { output: build.tag }
    timeout: 10m
    argocd:
      application: shop
      write_back:
        repo: gitops
        branch: main
        changes:
          - { file: apps/shop.yaml, image: team/shop, tag: { output: build.tag } }
    rollout: { name: shop, namespace: shop, until: paused }
    on_cancel: abort
  smoke:
    needs: deploy-prod
    image: x
    run: smoke
  approve:
    kind: approval
    needs: smoke
    deployment: deploy-prod
    message: Promote?
  promote:
    kind: rollout
    needs: approve
    deployment: deploy-prod
    action: promote
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [promote]
    abort_first: true
`

// waitPipeline watches until healthy and lets people promote at pauses.
var waitPipeline = strings.Replace(shopPipeline, "rollout: { name: shop, namespace: shop }",
	"rollout: { name: shop, namespace: shop, pauses: wait }\n    on_cancel: abort", 1)

// paused is a snapshot of the canary h2 paused indefinitely at step 1 of 3.
func paused() deploy.Snapshot {
	s := healthy(true)
	step := 1
	s.App.Health = "Suspended"
	ro := s.Rollout
	ro.Phase, ro.StableRS, ro.Step, ro.Paused, ro.PauseReasons, ro.PauseIndefinite = "Paused", "h1", &step, true, []string{"CanaryPauseStep"}, true
	ro.StepNames = []string{"weight 50%", "pause", "weight 100%"}
	w := 50
	ro.CanaryWeight = &w
	return s
}

// progressing is the canary h2 moving on at step 2.
func progressing() deploy.Snapshot {
	s := healthy(true)
	step := 2
	s.App.Health = "Progressing"
	s.Rollout.Phase, s.Rollout.StableRS, s.Rollout.Step = "Progressing", "h1", &step
	return s
}

// aborted is the canary h2 aborted.
func aborted() deploy.Snapshot {
	s := paused()
	s.App.Health = "Degraded"
	ro := s.Rollout
	ro.Phase, ro.Aborted, ro.Paused, ro.PauseReasons, ro.PauseIndefinite, ro.Message = "Degraded", true, false, nil, false, "RolloutAborted: aborted by a person"
	return s
}

var protectedEnv = []Environment{{Name: "prod", Target: "homelab", Projects: []string{"shop"}, Protected: true}}

// toPause runs a gated pipeline's deploy step until it stops at the pause.
func toPause(t *testing.T, e *Engine, id string) {
	t.Helper()
	build(t, e, id, "2")
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	if r := observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), paused()); !r.Accepted || r.Phase != deploy.PhasePaused {
		t.Fatalf("observation: %+v", r)
	}
}

func onlyDep(t *testing.T, e *Engine, id string) DeploymentView {
	t.Helper()
	ds := runDeployments(t, e, id)
	if len(ds) != 1 {
		t.Fatalf("deployments: %+v", ds)
	}
	return ds[0]
}

// takeCommand takes a command as the deploy runner.
func takeCommand(t *testing.T, e *Engine) *CommandJob {
	t.Helper()
	j, err := e.TakeCommand(context.Background(), deployer, deployer.Targets, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected a command to take")
	}
	return j
}

func noCommand(t *testing.T, e *Engine) {
	t.Helper()
	if j, err := e.TakeCommand(context.Background(), deployer, deployer.Targets, 0); err != nil || j != nil {
		t.Fatalf("expected no command, got %+v, %v", j, err)
	}
}

func finishCommand(t *testing.T, e *Engine, j *CommandJob, result string) {
	t.Helper()
	if err := e.FinishCommand(deployer, deployer.Targets, CommandResult{Take: j.Take, Result: result, Detail: "test"}); err != nil {
		t.Fatal(err)
	}
}

// openWatch is the watch of an open deployment that no step watches.
func openWatch(t *testing.T, e *Engine) WatchItem {
	t.Helper()
	items, _, err := e.Watch(context.Background(), deployer, deployer.Targets, "-", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Kind == WatchDeployment {
			return it
		}
	}
	t.Fatalf("no open deployment watched: %+v", items)
	return WatchItem{}
}

// The gated canary end to end: the deploy step stops at the pause, the
// approval waits while the open deployment is still observed, the rollout
// step gives its promote with what the rollout showed, a deploy runner
// applies it, and the rollout step ends when the rollout is healthy.
func TestGatedCanary(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, gatedPipeline, 1)
	if sum, _ := e.Summary(id); sum.MinSchema != schemaControl {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}
	toPause(t, e, id)
	want(t, statuses(t, e, id), "deploy-prod", "succeeded", "smoke", "queued")
	if d := onlyDep(t, e, id); d.PodHash != "h2" || !d.Holding || d.DeployedAt != 0 {
		t.Fatalf("deployment at the pause: %+v", d)
	}
	ok(t, e, take(t, e), nil) // smoke
	want(t, statuses(t, e, id), "approve", "waiting")
	// The approval is listed as one, and the deployment is still observed.
	if list, _ := e.Approvals(); len(list) != 1 || list[0].StepID != "approve" {
		t.Fatalf("approvals: %+v", list)
	}
	it := openWatch(t, e)
	if r := observe(t, e, deployer, it, paused()); !r.Accepted {
		t.Fatalf("open observation: %+v", r)
	}
	if err := e.Decide(id, "approve", true, "bob", "looks fine"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "promote", "waiting")
	pw := watchOf(t, e, deployer, "promote")
	if pw.Spec.Kind != pipeline.KindRolloutStep || pw.Spec.Until != "healthy" {
		t.Fatalf("promote's watch: %+v", pw.Spec)
	}
	noCommand(t, e) // the command comes from an observation
	if r := observe(t, e, deployer, pw, paused()); r.Phase != deploy.PhaseCommand {
		t.Fatalf("observation: %+v", r)
	}
	j := takeCommand(t, e)
	if j.Action != "promote" || j.Expect.PodHash != "h2" || j.Expect.Step == nil || *j.Expect.Step != 1 || j.Rollout.Name != "shop" || j.Actor != "step promote" {
		t.Fatalf("command job: %+v", j)
	}
	noCommand(t, e) // taken: nobody else gets it
	// Still paused (the runner has not patched yet): still waiting.
	observe(t, e, deployer, pw, paused())
	finishCommand(t, e, j, deploy.Applied)
	if r := observe(t, e, deployer, pw, progressing()); r.Phase != deploy.PhaseRollout {
		t.Fatalf("progressing: %+v", r)
	}
	want(t, statuses(t, e, id), "promote", "waiting")
	observe(t, e, deployer, pw, healthy(true))
	want(t, statuses(t, e, id), "run", "succeeded", "promote", "succeeded", "rollback-prod", "skipped")
	d := onlyDep(t, e, id)
	if d.Verdict != VerdictDeployed || d.DeployedAt == 0 || len(d.Commands) != 1 || d.Commands[0].Result != deploy.Applied {
		t.Fatalf("deployment: %+v", d)
	}
	envs, _ := e.EnvironmentStates(nil)
	if cur := envs[0].Services[0].Current; cur == nil || cur.Version != "2" {
		t.Fatalf("current: %+v", cur)
	}
}

// A person's promote, clicked twice: one command; the second click is told
// who asked. It can be withdrawn until a runner takes it, not after.
func TestPromoteTwiceAndWithdraw(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, waitPipeline, 1)
	build(t, e, id, "2")
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	it := watchOf(t, e, deployer, "deploy-prod")
	if r := observe(t, e, deployer, it, paused()); r.Phase != deploy.PhasePaused || !strings.Contains(r.Detail, "waiting for someone to promote") {
		t.Fatalf("observation: %+v", r)
	}
	dep := onlyDep(t, e, id).ID
	c1, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "alice", Comment: "canary fine"})
	if err != nil || c1.State != CommandPending || c1.Expect.PodHash != "h2" {
		t.Fatalf("first click: %+v %v", c1, err)
	}
	_, err = e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "bob"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already asked for by alice") {
		t.Fatalf("second click: %v", err)
	}
	if _, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote-full", Actor: "bob"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a full promotion while a promote waits: %v", err)
	}
	if err := e.WithdrawCommand(id, c1.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	noCommand(t, e)
	c2, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	j := takeCommand(t, e)
	if err := e.WithdrawCommand(id, c2.ID, "alice"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "can no longer be withdrawn") {
		t.Fatalf("withdrawing a taken command: %v", err)
	}
	finishCommand(t, e, j, deploy.Applied)
	// The same gate again, the rollout still shown paused there: done already.
	if _, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "carol"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already done") {
		t.Fatalf("promoting the same gate again: %v", err)
	}
	observe(t, e, deployer, it, healthy(true))
	want(t, statuses(t, e, id), "deploy-prod", "succeeded")
	cs := onlyDep(t, e, id).Commands
	if len(cs) != 2 || cs[0].State != CommandWithdrawn || cs[0].WithdrawnBy != "bob" || cs[1].Result != deploy.Applied || cs[1].TakenBy == "" {
		t.Fatalf("commands: %+v", cs)
	}
}

// On a protected environment the person who started the run cannot
// promote it, but can abort it; someone else can promote.
func TestProtectedEnvironmentSelfPromote(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	no := false
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: protectedEnv, AllowSelfApproval: &no})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, waitPipeline), Source: gitSource,
		Trigger: Trigger{Event: "manual", Ref: "refs/heads/main", Branch: "main", SHA: strings.Repeat("1", 40), Actor: "ann"}})
	if err != nil {
		t.Fatal(err)
	}
	id := r.ID
	build(t, e, id, "2")
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), paused())
	dep := onlyDep(t, e, id).ID
	_, err = e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "ann"})
	if !errors.Is(err, ErrForbidden) || !strings.Contains(err.Error(), "protected environment") {
		t.Fatalf("self-promote: %v", err)
	}
	_, ctls, err := e.Controls(id, dep, "ann")
	if err != nil || !ctls[0].Forbidden || ctls[0].Action != "promote" {
		t.Fatalf("controls: %+v %v", ctls, err)
	}
	if _, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "abort", Actor: "ann", Comment: "no"}); err != nil {
		t.Fatalf("self-abort: %v", err)
	}
	// Someone else may promote; the abort is first in line, though.
	if _, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "bob"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("promote behind an abort: %v", err)
	}
}

// An abort goes ahead of a promote nobody has taken, and rejects the
// approval that names the deployment; the run then rolls back, aborting
// first (the job carries the deployment's pod template hash).
func TestAbortRejectsLinkedApprovalAndRollsBack(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, gatedPipeline, 1)
	toPause(t, e, id)
	ok(t, e, take(t, e), nil) // smoke
	want(t, statuses(t, e, id), "approve", "waiting")
	dep := onlyDep(t, e, id).ID
	if _, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "abort", Actor: "carol", Comment: "error rate"}); err != nil {
		t.Fatal(err)
	}
	finishCommand(t, e, takeCommand(t, e), deploy.Applied)
	got := statuses(t, e, id)
	want(t, got, "approve", "failed", "promote", "skipped", "rollback-prod", "queued")
	if v := stepOf(t, e, id, "approve"); !strings.Contains(v.Error, "rejected automatically: the rollout was aborted") {
		t.Fatalf("approval: %q", v.Error)
	}
	j := leaseDeploy(t, e, deployer)
	if j.Deploy.Action != ActionRevert || !j.Deploy.AbortFirst || j.Deploy.PodHash != "h2" || j.Deploy.Rollout == nil {
		t.Fatalf("revert job: %+v", j.Deploy)
	}
}

// An approval is rejected by itself when the rollout is aborted by
// anything (here, its own analysis), as soon as it is observed.
func TestApprovalRejectedOnObservedAbort(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, gatedPipeline, 1)
	toPause(t, e, id)
	ok(t, e, take(t, e), nil)
	observe(t, e, deployer, openWatch(t, e), aborted())
	want(t, statuses(t, e, id), "approve", "failed", "rollback-prod", "queued")
}

// pauses: promote promotes each indefinite pause once; pauses: fail
// fails the step there.
func TestPausesPromoteAndFail(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	src := strings.Replace(waitPipeline, "pauses: wait", "pauses: promote", 1)
	id := createShop(t, e, src, 1)
	build(t, e, id, "2")
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	it := watchOf(t, e, deployer, "deploy-prod")
	observe(t, e, deployer, it, paused())
	observe(t, e, deployer, it, paused()) // the same pause again: no second command
	j := takeCommand(t, e)
	noCommand(t, e)
	if j.Action != "promote" || j.Actor != "step deploy-prod (pauses: promote)" {
		t.Fatalf("job: %+v", j)
	}
	finishCommand(t, e, j, deploy.Applied)
	observe(t, e, deployer, it, healthy(true))
	want(t, statuses(t, e, id), "deploy-prod", "succeeded")

	src = strings.Replace(waitPipeline, "pauses: wait", "pauses: fail", 1)
	id2 := createShop(t, e, src, 2)
	ok(t, e, take(t, e), nil) // the first run's smoke
	build(t, e, id2, "3")
	applied(t, e, leaseDeploy(t, e, deployer), "c3")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), paused())
	want(t, statuses(t, e, id2), "deploy-prod", "failed", "rollback-prod", "queued")
	if v := stepOf(t, e, id2, "deploy-prod"); !strings.Contains(v.Error, "pauses: fail") {
		t.Fatalf("reason %q", v.Error)
	}
}

// A runner that takes a command and never reports loses it once the take
// runs out; another takes it. After too many tries it fails, and a rollout
// step waiting for it fails too. A command that was not applied fails its
// rollout step.
func TestCommandTakenAgainAndNotApplied(t *testing.T) {
	e, clk := newDeployEngine(t, prodEnv)
	id := createShop(t, e, gatedPipeline, 1)
	toPause(t, e, id)
	ok(t, e, take(t, e), nil)
	if err := e.Decide(id, "approve", true, "bob", ""); err != nil {
		t.Fatal(err)
	}
	pw := watchOf(t, e, deployer, "promote")
	observe(t, e, deployer, pw, paused())
	j1 := takeCommand(t, e)
	clk.Add(ttl + time.Second)
	j2 := takeCommand(t, e)
	if j1.ID != j2.ID || j1.Take == j2.Take {
		t.Fatalf("takes: %+v %+v", j1, j2)
	}
	if err := e.FinishCommand(deployer, deployer.Targets, CommandResult{Take: j1.Take, Result: deploy.Applied}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a stale take reported: %v", err)
	}
	finishCommand(t, e, j2, deploy.NotApplied)
	want(t, statuses(t, e, id), "promote", "failed", "rollback-prod", "queued")
	if v := stepOf(t, e, id, "promote"); !strings.Contains(v.Error, "was not applied") {
		t.Fatalf("reason %q", v.Error)
	}
}

// Canceling a run with on_cancel: abort gives the rollout an abort, with
// the deployment's pod template hash; the deployment closes as abandoned.
func TestOnCancelAbort(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, waitPipeline, 1)
	build(t, e, id, "2")
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), paused())
	if err := e.Cancel(id, "dan"); err != nil {
		t.Fatal(err)
	}
	d := onlyDep(t, e, id)
	if d.Verdict != VerdictAbandoned || len(d.Commands) != 1 || d.Commands[0].Action != "abort" || d.Commands[0].Origin != OriginOnCancel || d.Commands[0].Expect.PodHash != "h2" {
		t.Fatalf("deployment: %+v", d)
	}
	// The run is over; the abort is still taken and recorded.
	j := takeCommand(t, e)
	finishCommand(t, e, j, deploy.Applied)
	if c := onlyDep(t, e, id).Commands[0]; c.Result != deploy.Applied {
		t.Fatalf("command: %+v", c)
	}
}

// A rollout step for retry after an abort step, end to end in the engine.
func TestAbortThenRetrySteps(t *testing.T) {
	src := strings.Replace(gatedPipeline, `  promote:
    kind: rollout
    needs: approve
    deployment: deploy-prod
    action: promote
`, `  stop:
    kind: rollout
    needs: approve
    deployment: deploy-prod
    action: abort
  again:
    kind: rollout
    needs: stop
    deployment: deploy-prod
    action: retry
    until: paused
  promote:
    kind: rollout
    needs: again
    deployment: deploy-prod
    action: promote
`, 1)
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, src, 1)
	toPause(t, e, id)
	ok(t, e, take(t, e), nil)
	if err := e.Decide(id, "approve", true, "bob", ""); err != nil {
		t.Fatal(err)
	}
	sw := watchOf(t, e, deployer, "stop")
	observe(t, e, deployer, sw, paused())
	finishCommand(t, e, takeCommand(t, e), deploy.Applied)
	observe(t, e, deployer, sw, aborted())
	want(t, statuses(t, e, id), "stop", "succeeded", "again", "waiting")
	aw := watchOf(t, e, deployer, "again")
	observe(t, e, deployer, aw, aborted())
	j := takeCommand(t, e)
	if j.Action != "retry" {
		t.Fatalf("job %+v", j)
	}
	finishCommand(t, e, j, deploy.Applied)
	observe(t, e, deployer, aw, aborted()) // read before the retry reached the rollout
	want(t, statuses(t, e, id), "again", "waiting")
	observe(t, e, deployer, aw, paused())
	want(t, statuses(t, e, id), "again", "succeeded", "promote", "waiting")
}

// Idle services are observed only by runners that say they can, and
// their drift is kept.
func TestIdleServiceDrift(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), healthy(true))
	ok(t, e, take(t, e), nil)
	if items, _, _ := e.Watch(context.Background(), deployer, deployer.Targets, "-", 0); len(items) != 0 {
		t.Fatalf("an older runner got idle services: %+v", items)
	}
	idle := deployer
	idle.Capabilities = []string{CapabilityIdle}
	items, _, err := e.Watch(context.Background(), idle, idle.Targets, "-", 0)
	if err != nil || len(items) != 1 || items[0].Kind != WatchUnit || items[0].Spec.Commit != "c1" || items[0].Spec.Rollout == nil {
		t.Fatalf("idle items: %+v %v", items, err)
	}
	if r := observe(t, e, idle, items[0], healthy(true)); !r.Accepted || r.Phase != "in_line" {
		t.Fatalf("in line: %+v", r)
	}
	s := healthy(true)
	s.App.Sync = "OutOfSync"
	s.Rollout.CurrentPodHash, s.Rollout.StableRS = "h9", "h9"
	observe(t, e, idle, items[0], s)
	envs, _ := e.EnvironmentStates(nil)
	l := envs[0].Services[0].Live
	if l == nil || l.Drift != DriftDiffers || !strings.Contains(l.DriftDetail, "h9") || l.ObservedBy != deployer.Name {
		t.Fatalf("live: %+v", l)
	}
}

// Two servers, one deployment, two people clicking Promote at the same
// moment: exactly one command.
func TestPromoteTwiceOnTwoServers(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	a, b := openDeploy(t, addr, clk, prodEnv), openDeploy(t, addr, clk, prodEnv)
	for round := range 5 {
		id := createShop(t, a, waitPipeline, round+1)
		build(t, a, id, "2")
		applied(t, a, leaseDeploy(t, a, deployer), "c2")
		observe(t, b, deployer, watchOf(t, a, deployer, "deploy-prod"), paused())
		dep := onlyDep(t, a, id).ID
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, e := range []*Engine{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: []string{"alice", "bob"}[i]})
			}()
		}
		wg.Wait()
		okCount := 0
		for _, err := range errs {
			switch {
			case err == nil:
				okCount++
			case !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "already asked for by"):
				t.Fatalf("round %d: %v", round, err)
			}
		}
		if cs := onlyDep(t, b, id).Commands; okCount != 1 || len(cs) != 1 {
			t.Fatalf("round %d: %d accepted, commands %+v", round, okCount, cs)
		}
		finishCommand(t, b, takeCommand(t, a), deploy.Applied)
		observe(t, a, deployer, watchOf(t, b, deployer, "deploy-prod"), healthy(true))
		ok(t, a, take(t, a), nil) // smoke
	}
}

// A build limited to schema 13 leaves alone runs with rollout steps, and
// runs that got a command, while a schema-14 server finishes them.
func TestOlderBuildLeavesControlledRunsAlone(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	cur := openDeploy(t, addr, clk, prodEnv)
	gated := createShop(t, cur, gatedPipeline, 1)
	toPause(t, cur, gated)
	var buf syncBuffer
	old := mustOpenAs(t, addr, clk, schemaControl-1, &buf)
	defer old.Close()
	for _, id := range []string{gated} {
		d, err := old.GetRun(id)
		if err != nil || !d.NeedsNewerServer {
			t.Fatalf("older build's view of %s: %v %+v", id, err, d)
		}
		if err := old.Cancel(id, "ann"); !errors.Is(err, ErrNeedsNewer) {
			t.Fatalf("older build canceled %s: %v", id, err)
		}
	}
	// An older server cannot create such a run.
	if _, err := old.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, gatedPipeline), Source: gitSource}); err == nil {
		t.Fatal("older build created a run with rollout steps")
	}
	// A run of deploy steps only, which the older build understands, until a
	// command is given to its deployment.
	id := createShop(t, cur, shopPipeline, 3)
	ok(t, cur, take(t, cur), nil) // the gated run's smoke
	build(t, cur, id, "3")
	if d, _ := old.GetRun(id); d.NeedsNewerServer {
		t.Fatalf("older build does not understand a plain deploy run: %v", d.NotUnderstood)
	}
	// Its own runs and other deploy runs go on; no command or approval of
	// the gated run is touched by its reaper.
	clk.Add(25 * time.Hour)
	old.expireWaits()
	old.reap()
	if s := stepOf(t, cur, gated, "approve"); s.Status != StepWaitingS {
		t.Fatalf("the older build changed the approval: %s", s.Status)
	}
	cur.expireWaits()
	if s := stepOf(t, cur, gated, "approve"); s.Status != StepFailed {
		t.Fatalf("the current build did not expire the approval: %s", s.Status)
	}
}

// A command on a run the older build understands raises its min_schema,
// so the older build leaves it alone from then on.
func TestCommandRaisesMinSchema(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	cur := openDeploy(t, addr, clk, prodEnv)
	id := createShop(t, cur, shopPipeline, 1) // phase-1 keys only
	build(t, cur, id, "2")
	applied(t, cur, leaseDeploy(t, cur, deployer), "c2")
	observe(t, cur, deployer, watchOf(t, cur, deployer, "deploy-prod"), paused())
	if sum, _ := cur.Summary(id); sum.MinSchema != schemaDeploy {
		t.Fatalf("min_schema before %d", sum.MinSchema)
	}
	dep := onlyDep(t, cur, id).ID
	if _, err := cur.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	if sum, _ := cur.Summary(id); sum.MinSchema != schemaControl {
		t.Fatalf("min_schema after %d", sum.MinSchema)
	}
	var buf syncBuffer
	old := mustOpenAs(t, addr, clk, schemaControl-1, &buf)
	defer old.Close()
	if d, _ := old.GetRun(id); !d.NeedsNewerServer {
		t.Fatal("older build understands a run with a command")
	}
	if items, _, err := old.Watch(context.Background(), deployer, deployer.Targets, "-", 0); err != nil || len(items) != 0 {
		t.Fatalf("older build watches it: %+v %v", items, err)
	}
}

// The pipeline package's marker and the engine's schema are the same.
func TestSchemaControlMatchesPipeline(t *testing.T) {
	if pipeline.SchemaControl != schemaControl || len(migrations) < schemaControl {
		t.Fatalf("pipeline says %d, the engine %d (%d migrations)", pipeline.SchemaControl, schemaControl, len(migrations))
	}
}
