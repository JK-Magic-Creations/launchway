package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/hub"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// shopPipeline builds, deploys to prod, smoke-tests, and rolls back if the
// smoke test (or the deployment itself) fails.
const shopPipeline = `pipeline: shop
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
    rollout: { name: shop, namespace: shop }
  smoke:
    needs: deploy-prod
    image: x
    run: smoke
  rollback-prod:
    kind: rollback
    deployment: deploy-prod
    needs: [smoke]
`

var prodEnv = []Environment{{Name: "prod", Target: "cluster-a", Projects: []string{"shop"}}}

var deployer = RunnerInfo{ID: "deployer-1", Name: "deployer one", Targets: []string{"cluster-a"}, Pool: "deploy"}

func openDeploy(t *testing.T, addr string, clk *clock, envs []Environment) *Engine {
	t.Helper()
	e, err := Open(Options{Database: addr, LeaseTTL: ttl, Now: clk.Now, Environments: envs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func newDeployEngine(t *testing.T, envs []Environment) (*Engine, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	return openDeploy(t, testdb.New(t), clk, envs), clk
}

var gitSource = Source{Kind: "git", URL: "https://git.example/shop.git"}

func createShop(t *testing.T, e *Engine, src string, number int) string {
	t.Helper()
	sha := fmt.Sprintf("%040d", number)
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, src), Source: gitSource,
		Trigger: Trigger{Event: "push", Ref: "refs/heads/main", Branch: "main", SHA: sha}})
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// build runs the run's build step, which writes the tag. Other queued
// steps leased on the way are handed back.
func build(t *testing.T, e *Engine, runID, tag string) {
	t.Helper()
	var back []*Job
	defer func() {
		for _, j := range back {
			if err := e.Release(j.LeaseID); err != nil && !errors.Is(err, ErrLeaseLost) {
				t.Fatal(err)
			}
		}
	}()
	for range 10 {
		j, err := e.Lease(context.Background(), runner, 0)
		if err != nil || j == nil {
			t.Fatalf("no build step of %s to lease: %v", runID, err)
		}
		if j.RunID != runID || j.StepID != "build" {
			back = append(back, j)
			continue
		}
		if _, err := e.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		ok(t, e, j, map[string]string{"tag": tag})
		return
	}
	t.Fatalf("could not find the build step of %s", runID)
}

func leaseDeploy(t *testing.T, e *Engine, r RunnerInfo) *Job {
	t.Helper()
	j, err := e.LeaseDeploy(context.Background(), r, r.Targets, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected a deploy action, found none")
	}
	if _, err := e.Start(j.LeaseID); err != nil {
		t.Fatal(err)
	}
	return j
}

func noDeploy(t *testing.T, e *Engine, r RunnerInfo) {
	t.Helper()
	j, err := e.LeaseDeploy(context.Background(), r, r.Targets, 0)
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Fatalf("expected no deploy action, got %s/%s", j.RunID, j.StepID)
	}
}

// applied finishes an apply action as a deploy runner that committed.
func applied(t *testing.T, e *Engine, j *Job, commit string) {
	t.Helper()
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Deploy: &DeployFacts{Repo: "gitops", Branch: "main", Commit: commit, Changed: true,
		Sync: "not needed: the application syncs by itself"}}); err != nil {
		t.Fatal(err)
	}
}

func watchOf(t *testing.T, e *Engine, r RunnerInfo, step string) WatchItem {
	t.Helper()
	items, _, err := e.Watch(context.Background(), r, r.Targets, "-", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Step == step {
			return it
		}
	}
	t.Fatalf("%s is not watched; watched: %+v", step, items)
	return WatchItem{}
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// healthy is a snapshot of a deployment that is done.
func healthy(commitSynced bool) deploy.Snapshot {
	c := yes()
	if !commitSynced {
		c = no()
	}
	step, weight := 3, 100
	return deploy.Snapshot{
		App:     &deploy.App{Found: true, Sync: "Synced", Health: "Healthy", Revision: "abc123", Contains: c, Automated: true},
		Rollout: &deploy.Rollout{Found: true, Phase: "Healthy", ObservedKnown: true, CurrentPodHash: "h2", StableRS: "h2", Step: &step, Steps: 3, CanaryWeight: &weight, Replicas: 2, Ready: 2},
	}
}

func degraded() deploy.Snapshot {
	s := healthy(true)
	s.App.Health = "Degraded"
	s.Rollout.Phase, s.Rollout.Aborted, s.Rollout.StableRS, s.Rollout.Message = "Degraded", true, "h1", "RolloutAborted: metric smoke failed"
	s.Analysis = []deploy.AnalysisRun{{Name: "shop-h2-2", Phase: "Failed", PodHash: "h2",
		Metrics: []deploy.Metric{{Name: "smoke", Phase: "Failed", Count: 1, Failed: 1}}}}
	return s
}

func observe(t *testing.T, e *Engine, r RunnerInfo, it WatchItem, s deploy.Snapshot) ObserveResult {
	t.Helper()
	res, err := e.Observe(r, r.Targets, []Observation{{Key: it.Key, Snapshot: s}})
	if err != nil {
		t.Fatal(err)
	}
	return res[0]
}

func runDeployments(t *testing.T, e *Engine, runID string) []DeploymentView {
	t.Helper()
	ds, err := e.RunDeployments(runID)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func lockRows(t *testing.T, e *Engine) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM deploy_locks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeployHappyPath(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1.4.2")
	want(t, statuses(t, e, id), "deploy-prod", "queued")

	// Ordinary runners never get deploy work; a runner of another target
	// neither.
	none(t, e)
	noDeploy(t, e, RunnerInfo{ID: "other", Targets: []string{"elsewhere"}})

	j := leaseDeploy(t, e, deployer)
	d := j.Deploy
	if d == nil || d.Action != ActionApply || d.Version != "1.4.2" || d.Target != "cluster-a" || d.Application != "shop" ||
		len(d.Changes) != 1 || d.Changes[0].Tag != "1.4.2" || d.Changes[0].Image != "team/shop" || !strings.Contains(d.Message, "Deploy shop 1.4.2 to prod") {
		t.Fatalf("job: %+v", d)
	}
	if j.Workspace != "" || j.Image != "" {
		t.Fatalf("a deploy job needs no workspace or image: %+v", j)
	}
	// The action does not move the run to the deploy runner.
	if st, _ := e.State(id); st.Runner == deployer.ID {
		t.Fatal("the deploy runner took the run's workspace")
	}
	if err := e.AppendLogs(j.LeaseID, []LogLine{{Text: "committed"}}); err != nil {
		t.Fatal(err)
	}
	applied(t, e, j, "c0ffee")
	want(t, statuses(t, e, id), "deploy-prod", "waiting")
	if v := stepOf(t, e, id, "deploy-prod"); v.WaitKind != WaitObserve {
		t.Fatalf("wait kind %q", v.WaitKind)
	}
	if list, _ := e.Approvals(); len(list) != 0 {
		t.Fatalf("a deploy step is listed as an approval: %+v", list)
	}

	it := watchOf(t, e, deployer, "deploy-prod")
	if it.Spec.Commit != "c0ffee" || it.Spec.Rollout == nil || it.Repo != "gitops" || it.Application != "shop" {
		t.Fatalf("watch: %+v", it)
	}
	// Not synced yet: nothing changes but the phase.
	if r := observe(t, e, deployer, it, healthy(false)); !r.Accepted || r.Phase != deploy.PhaseSyncing {
		t.Fatalf("observation: %+v", r)
	}
	want(t, statuses(t, e, id), "deploy-prod", "waiting")
	if r := observe(t, e, deployer, it, healthy(true)); !r.Accepted || r.Phase != deploy.PhaseHealthy {
		t.Fatalf("observation: %+v", r)
	}
	want(t, statuses(t, e, id), "deploy-prod", "succeeded", "smoke", "queued")
	if v := stepOf(t, e, id, "deploy-prod"); v.Outputs["commit"] != "c0ffee" || v.Outputs["version"] != "1.4.2" {
		t.Fatalf("outputs: %v", v.Outputs)
	}
	// A later step gets them.
	sj := take(t, e)
	if sj.Env["LAUNCHWAY_NEEDS_DEPLOY_PROD_COMMIT"] != "c0ffee" {
		t.Fatalf("smoke's env: %v", sj.Env)
	}
	// The lock is held until the smoke test (a step of the deployment) ends.
	if lockRows(t, e) != 1 {
		t.Fatal("the lock was released before the deployment's steps ended")
	}
	ok(t, e, sj, nil)
	got := statuses(t, e, id)
	want(t, got, "run", "succeeded", "rollback-prod", "skipped")
	if v := stepOf(t, e, id, "rollback-prod"); v.SkipKind != SkipNotNeeded {
		t.Fatalf("rollback skip kind %q", v.SkipKind)
	}
	ds := runDeployments(t, e, id)
	if len(ds) != 1 || ds[0].Verdict != VerdictDeployed || ds[0].Commit != "c0ffee" || ds[0].Holding || len(ds[0].Timeline) < 2 {
		t.Fatalf("deployments: %+v", ds)
	}
	if lockRows(t, e) != 0 {
		t.Fatal("the lock was not released")
	}
	envs, err := e.EnvironmentStates(nil)
	if err != nil || len(envs) != 1 || len(envs[0].Services) != 1 || envs[0].Services[0].Current == nil ||
		envs[0].Services[0].Current.Version != "1.4.2" || envs[0].Services[0].Service != "shop" {
		t.Fatalf("environments: %+v, %v", envs, err)
	}
	// Watches end with the step.
	if items, _, _ := e.Watch(context.Background(), deployer, deployer.Targets, "-", 0); len(items) != 0 {
		t.Fatalf("still watched: %+v", items)
	}
}

// A failed smoke test rolls the deployment back: the rollback step reverts,
// and once Argo CD is healthy on the revert the environment runs the
// previous version again.
func TestRollbackAfterSmokeFails(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	first := createShop(t, e, shopPipeline, 1)
	deployToHealthy(t, e, first, "1.0.0", "c1")
	ok(t, e, take(t, e), nil) // smoke

	id := createShop(t, e, shopPipeline, 2)
	deployToHealthy(t, e, id, "1.1.0", "c2")
	fail(t, e, take(t, e)) // smoke fails
	want(t, statuses(t, e, id), "smoke", "failed", "rollback-prod", "queued")
	j := leaseDeploy(t, e, deployer)
	if j.StepID != "rollback-prod" || j.Deploy.Action != ActionRevert || j.Deploy.Commit != "c2" || j.Deploy.PreviousVersion != "1.0.0" ||
		!strings.Contains(j.Deploy.Message, "Roll back shop in prod to 1.0.0") {
		t.Fatalf("revert job: %+v", j.Deploy)
	}
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Deploy: &DeployFacts{Commit: "c2", Revert: "r2", Reverted: true, Changed: true}}); err != nil {
		t.Fatal(err)
	}
	it := watchOf(t, e, deployer, "rollback-prod")
	if it.Spec.Commit != "r2" || it.Spec.Kind != pipeline.KindRollback {
		t.Fatalf("rollback watch: %+v", it)
	}
	observe(t, e, deployer, it, healthy(true))
	want(t, statuses(t, e, id), "run", "failed", "rollback-prod", "succeeded")
	ds := runDeployments(t, e, id)
	if ds[0].Verdict != VerdictRolledBack || ds[0].Revert != "r2" || ds[0].PreviousVersion != "1.0.0" {
		t.Fatalf("deployment: %+v", ds[0])
	}
	envs, _ := e.EnvironmentStates(nil)
	if cur := envs[0].Services[0].Current; cur == nil || cur.Version != "1.0.0" || cur.Run != first {
		t.Fatalf("after the rollback the environment runs %+v", cur)
	}
}

// deployToHealthy builds and deploys a run until its deploy step succeeds.
func deployToHealthy(t *testing.T, e *Engine, id, tag, commit string) {
	t.Helper()
	build(t, e, id, tag)
	applied(t, e, leaseDeploy(t, e, deployer), commit)
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), healthy(true))
	want(t, statuses(t, e, id), "deploy-prod", "succeeded")
}

// A deployment that fails (the rollout aborts) skips the smoke test, and
// the rollback runs because of it.
func TestDegradedDeploymentIsRolledBack(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "2.0.0")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	r := observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), degraded())
	if r.Phase != deploy.PhaseAborted || !strings.Contains(r.Detail, "metric smoke failed") {
		t.Fatalf("observation: %+v", r)
	}
	want(t, statuses(t, e, id), "deploy-prod", "failed", "smoke", "skipped", "rollback-prod", "queued")
	if v := stepOf(t, e, id, "deploy-prod"); !strings.Contains(v.Error, "aborted") {
		t.Fatalf("reason: %q", v.Error)
	}
	// A deployment Git already held changes nothing: there is nothing to
	// revert, and the rollback says so.
	j := leaseDeploy(t, e, deployer)
	if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", Deploy: &DeployFacts{}}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "failed", "rollback-prod", "succeeded")
	if ds := runDeployments(t, e, id); ds[0].Verdict != VerdictRolledBack || lockRows(t, e) != 0 {
		t.Fatalf("deployment: %+v", ds[0])
	}
}

// A step that fails and is allowed to (on_failure: continue) is, for the
// steps that need it, a success: it does not roll the deployment back.
func TestAllowedFailureDoesNotRollBack(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	first := createShop(t, e, shopPipeline, 1)
	deployToHealthy(t, e, first, "1.0.0", "c1")
	ok(t, e, take(t, e), nil) // smoke

	tolerant := strings.Replace(shopPipeline, "    run: smoke\n", "    run: smoke\n    on_failure: continue\n", 1)
	id := createShop(t, e, tolerant, 2)
	deployToHealthy(t, e, id, "1.1.0", "c2")
	fail(t, e, take(t, e)) // smoke fails, and is allowed to
	want(t, statuses(t, e, id), "run", "succeeded", "smoke", "failed", "rollback-prod", "skipped")
	if v := stepOf(t, e, id, "rollback-prod"); v.SkipKind != SkipNotNeeded {
		t.Fatalf("rollback skip kind %q", v.SkipKind)
	}
}

// A revert that conflicts fails the rollback: the environment needs a
// person, and the deployment says so.
func TestRevertConflictNeedsAPerson(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	deployToHealthy(t, e, id, "1.0.0", "c1")
	fail(t, e, take(t, e))
	j := leaseDeploy(t, e, deployer)
	if err := e.Finish(j.LeaseID, Result{Outcome: "failed", Error: "the revert of c1 does not apply to main"}); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "failed", "rollback-prod", "failed")
	if ds := runDeployments(t, e, id); ds[0].Verdict != VerdictRollbackFailed {
		t.Fatalf("verdict %q", ds[0].Verdict)
	}
	// A deployment can never be resumed; a re-run from the deploy step can
	// be made.
	if err := e.Resume(id, "ann"); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "never continued") || !strings.Contains(err.Error(), `Re-run from deploy-prod`) {
		t.Fatalf("resume must name the action that works: %v", err)
	}
	d, err := e.GetRun(id)
	if err != nil || d.Resume == nil || d.Resume.Allowed || d.Resume.RerunFrom != "deploy-prod" {
		t.Fatalf("the run says what to do instead: %+v %v", d.Resume, err)
	}
	if _, err := e.PlanRerun(id, "smoke"); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-run from inside the deployment: %v", err)
	}
	if _, err := e.PlanRerun(id, "deploy-prod"); err != nil {
		t.Fatalf("re-run from the deploy step: %v", err)
	}
}

// Second deploy of the same service waits for the lock and gets it when
// the first deployment's steps end.
func TestLockWaitsAndHandsOver(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	a := createShop(t, e, shopPipeline, 1)
	b := createShop(t, e, shopPipeline, 2)
	build(t, e, a, "1")
	build(t, e, b, "2")
	want(t, statuses(t, e, a), "deploy-prod", "queued")
	want(t, statuses(t, e, b), "deploy-prod", "waiting")
	if v := stepOf(t, e, b, "deploy-prod"); v.WaitKind != WaitLock {
		t.Fatalf("wait kind %q", v.WaitKind)
	}
	envs, _ := e.EnvironmentStates(nil)
	if u := envs[0].Services[0]; u.Lock == nil || u.Lock.Run != a || len(u.Waiting) != 1 || u.Waiting[0].Run != b {
		t.Fatalf("environment: %+v", u)
	}
	deployA := leaseDeploy(t, e, deployer)
	noDeploy(t, e, deployer)
	applied(t, e, deployA, "c1")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), healthy(true))
	ok(t, e, take(t, e), nil) // a's smoke
	want(t, statuses(t, e, a), "run", "succeeded")
	e.dispatchLocks()
	want(t, statuses(t, e, b), "deploy-prod", "queued")
	j := leaseDeploy(t, e, deployer)
	if j.RunID != b || j.Deploy.PreviousVersion != "1" {
		t.Fatalf("b's job: %+v", j.Deploy)
	}
}

// With queue: newest, a run waiting for the lock is skipped when a newer one
// starts waiting; with queue: all, they deploy in turn.
func TestQueueNewestAndAll(t *testing.T) {
	for _, queue := range []string{"newest", "all"} {
		t.Run(queue, func(t *testing.T) {
			e, _ := newDeployEngine(t, []Environment{{Name: "prod", Target: "cluster-a", Projects: []string{"shop"}, Queue: queue}})
			runs := []string{createShop(t, e, shopPipeline, 1), createShop(t, e, shopPipeline, 2), createShop(t, e, shopPipeline, 3)}
			for i, id := range runs {
				build(t, e, id, fmt.Sprint(i+1))
			}
			want(t, statuses(t, e, runs[0]), "deploy-prod", "queued")
			want(t, statuses(t, e, runs[2]), "deploy-prod", "waiting")
			if queue == "newest" {
				want(t, statuses(t, e, runs[1]), "run", "succeeded", "deploy-prod", "skipped", "rollback-prod", "skipped")
				if v := stepOf(t, e, runs[1], "deploy-prod"); v.SkipKind != SkipSuperseded || v.Error != "superseded at prod by run #3" {
					t.Fatalf("superseded step: %q %q", v.SkipKind, v.Error)
				}
			} else {
				want(t, statuses(t, e, runs[1]), "deploy-prod", "waiting")
			}
			// The first deployment ends; the next waiter takes the lock: the
			// newest under newest, the oldest under all.
			applied(t, e, leaseDeploy(t, e, deployer), "c1")
			observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), healthy(true))
			ok(t, e, take(t, e), nil)
			e.dispatchLocks()
			next := runs[2]
			if queue == "all" {
				next = runs[1]
			}
			j := leaseDeploy(t, e, deployer)
			if j.RunID != next {
				t.Fatalf("the lock went to %s, want %s", j.RunID, next)
			}
		})
	}
}

// A newer run superseding older ones never cancels one that holds an open
// deployment: it finishes, and the newer run waits for the lock.
func TestSupersedeSparesOpenDeployments(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	src := "supersede: true\non: { push: { branches: [main] } }\n" + strings.TrimPrefix(shopPipeline, "")
	a := createShop(t, e, src, 1)
	build(t, e, a, "1")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	waiting := createShop(t, e, src, 2) // a newer push, still building
	want(t, statuses(t, e, a), "run", "running", "deploy-prod", "waiting")
	if sum, _ := e.Summary(a); sum.CancelRequested {
		t.Fatal("the run with an open deployment was canceled")
	}
	// A run that has not opened a deployment yet is superseded as usual.
	c := createShop(t, e, src, 3)
	want(t, statuses(t, e, waiting), "run", "canceled")
	build(t, e, c, "3")
	want(t, statuses(t, e, c), "deploy-prod", "waiting")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), healthy(true))
	want(t, statuses(t, e, a), "deploy-prod", "succeeded")
}

// The deadline and a healthy snapshot arriving at the same moment: exactly
// one of them decides, and the step's events say which.
func TestDeadlineRacesAHealthySnapshot(t *testing.T) {
	for i := range 20 {
		e, clk := newDeployEngine(t, prodEnv)
		id := createShop(t, e, shopPipeline, 1)
		build(t, e, id, "1")
		applied(t, e, leaseDeploy(t, e, deployer), "c1")
		it := watchOf(t, e, deployer, "deploy-prod")
		observe(t, e, deployer, it, healthy(false))
		clk.Add(10*time.Minute + time.Duration(i%3-1)*time.Millisecond) // just before, at, or after
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			e.Observe(deployer, deployer.Targets, []Observation{{Key: it.Key, Snapshot: healthy(true)}})
		}()
		go func() { defer wg.Done(); e.expireWaits() }()
		wg.Wait()
		e.expireWaits()
		st, _ := e.State(id)
		s := st.Steps["deploy-prod"]
		finished := countEvents(t, e, id, StepFinished)
		if s.Status.Terminal() == false {
			t.Fatalf("try %d: the step is %s after its deadline", i, s.Status)
		}
		// build + exactly one ending of deploy-prod
		if finished != 2 {
			t.Fatalf("try %d: %d step.finished events", i, finished)
		}
		if s.Status == StepFailed && !strings.Contains(s.Error, "not healthy within 10m") {
			t.Fatalf("try %d: reason %q", i, s.Error)
		}
	}
}

// Only the observer of a target may report for it; another runner takes
// over when the observer's lease runs out, and a snapshot of an earlier
// watch is never taken for the current one.
func TestObserverTakeover(t *testing.T) {
	e, clk := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	it := watchOf(t, e, deployer, "deploy-prod")
	other := RunnerInfo{ID: "deployer-2", Name: "deployer two", Targets: []string{"cluster-a"}, Pool: "deploy"}
	if items, _, _ := e.Watch(context.Background(), other, other.Targets, "-", 0); len(items) != 0 {
		t.Fatalf("a second runner observes too: %+v", items)
	}
	if r := observe(t, e, other, it, healthy(true)); r.Accepted || !strings.Contains(r.Reason, "not the observer") {
		t.Fatalf("observation by a runner that is not the observer: %+v", r)
	}
	want(t, statuses(t, e, id), "deploy-prod", "waiting")
	// The first runner goes quiet.
	clk.Add(ttl + time.Second)
	it2 := watchOf(t, e, other, "deploy-prod")
	if it2.Key != it.Key {
		t.Fatalf("the watch changed: %s, %s", it.Key, it2.Key)
	}
	if r := observe(t, e, deployer, it, healthy(true)); r.Accepted {
		t.Fatalf("the old observer was still heard: %+v", r)
	}
	if r := observe(t, e, other, it2, deploy.Snapshot{Error: "dial tcp: connection refused"}); !r.Accepted || r.Phase != deploy.PhaseUnobserved {
		t.Fatalf("unreachable: %+v", r)
	}
	if r := observe(t, e, other, WatchItem{Key: strings.Replace(it.Key, "/c1", "/c0", 1)}, healthy(true)); r.Accepted {
		t.Fatalf("a snapshot for another commit was taken: %+v", r)
	}
	observe(t, e, other, it2, healthy(true))
	want(t, statuses(t, e, id), "deploy-prod", "succeeded")
}

// A deploy runner that dies during an action loses it; the action is
// queued again (a new attempt), and the retry is idempotent on the runner.
func TestLostActionIsTriedAgain(t *testing.T) {
	e, clk := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	j := leaseDeploy(t, e, deployer)
	if err := e.AppendLogs(j.LeaseID, []LogLine{{Text: "pushing"}}); err != nil {
		t.Fatal(err)
	}
	clk.Add(ttl + time.Second)
	e.reap()
	want(t, statuses(t, e, id), "deploy-prod", "queued")
	j2 := leaseDeploy(t, e, deployer)
	if j2.Attempt != j.Attempt+1 || j2.Deploy.Deployment != j.Deploy.Deployment {
		t.Fatalf("retry: attempt %d (was %d), deployment %s (was %s)", j2.Attempt, j.Attempt, j2.Deploy.Deployment, j.Deploy.Deployment)
	}
	applied(t, e, j2, "c1")
	want(t, statuses(t, e, id), "deploy-prod", "waiting")
}

// Canceling a run while it watches a deployment closes the deployment as
// abandoned and frees the lock.
func TestCancelAbandonsTheDeployment(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	if err := e.Cancel(id, "ann"); err != nil {
		t.Fatal(err)
	}
	want(t, statuses(t, e, id), "run", "canceled", "deploy-prod", "canceled", "rollback-prod", "canceled")
	if ds := runDeployments(t, e, id); ds[0].Verdict != VerdictAbandoned || lockRows(t, e) != 0 {
		t.Fatalf("deployment: %+v", ds[0])
	}
}

// Nobody serving the target: the step fails at its deadline, saying so.
func TestNoDeployRunnerTimesOut(t *testing.T) {
	e, clk := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	clk.Add(11 * time.Minute)
	e.expireWaits()
	want(t, statuses(t, e, id), "deploy-prod", "failed", "rollback-prod", "queued")
	if v := stepOf(t, e, id, "deploy-prod"); !strings.Contains(v.Error, "no deploy runner serving target cluster-a") {
		t.Fatalf("reason %q", v.Error)
	}
}

// Deploy steps never run in drafts, pull-request runs or local runs, and a
// pipeline naming an environment the server does not have is refused with
// the line.
func TestWhereDeployStepsDoNotRun(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	p := parse(t, shopPipeline)
	r, err := e.CreateRun(NewRun{Project: "shop", Pipeline: p, Source: Source{Kind: "dir", Path: "/tmp"}})
	if err != nil {
		t.Fatal(err)
	}
	build(t, e, r.ID, "1")
	want(t, statuses(t, e, r.ID), "deploy-prod", "failed", "rollback-prod", "skipped")
	if v := stepOf(t, e, r.ID, "deploy-prod"); !strings.Contains(v.Error, "only on a server") {
		t.Fatalf("reason %q", v.Error)
	}

	d, err := e.CreateRun(NewRun{Project: "shop", Pipeline: p, Source: gitSource, Draft: &Draft{Text: shopPipeline}, Trigger: Trigger{Branch: "main", SHA: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	// A draft does not even reach the deploy step: its when-less steps run,
	// and the deploy step fails before taking any lock.
	build(t, e, d.ID, "1")
	if v := stepOf(t, e, d.ID, "deploy-prod"); !strings.Contains(v.Error, "draft") {
		t.Fatalf("draft reason %q", v.Error)
	}
	if lockRows(t, e) != 0 {
		t.Fatal("a refused step took a lock")
	}

	_, err = e.CreateRun(NewRun{Project: "other", Pipeline: p, Source: gitSource})
	var ps pipeline.Problems
	if !errors.As(err, &ps) || ps[0].Line != 9 || !strings.Contains(ps[0].Message, "project other may not deploy to prod") {
		t.Fatalf("a project not allowed: %v", err)
	}
	staging := strings.Replace(shopPipeline, "environment: prod", "environment: staging", 1)
	_, err = e.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, staging), Source: gitSource})
	if !errors.As(err, &ps) || !strings.Contains(ps[0].Message, `environment "staging" is not configured on this server; it knows prod`) {
		t.Fatalf("an unknown environment: %v", err)
	}
}

// Missing outputs fail the deploy step before it takes the lock, naming
// what is missing.
func TestMissingOutputFailsBeforeTheLock(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	ok(t, e, take(t, e), nil) // build writes no tag
	want(t, statuses(t, e, id), "deploy-prod", "failed")
	if v := stepOf(t, e, id, "deploy-prod"); !strings.Contains(v.Error, "build did not write the output tag") {
		t.Fatalf("reason %q", v.Error)
	}
	if lockRows(t, e) != 0 {
		t.Fatal("a lock was taken")
	}
}

// Two servers deploying the same service at once: exactly one opens a
// deployment; the other waits, and gets the lock when it is released.
func TestLockContentionAcrossServers(t *testing.T) {
	if !testdb.Postgres() {
		t.Skip("needs PostgreSQL: set " + testdb.EnvVar)
	}
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	a, b := openDeploy(t, addr, clk, prodEnv), openDeploy(t, addr, clk, prodEnv)
	for round := range 5 {
		r1 := createShop(t, a, shopPipeline, 2*round+1)
		r2 := createShop(t, b, shopPipeline, 2*round+2)
		j1, j2 := take(t, a), take(t, b)
		var wg sync.WaitGroup
		wg.Add(2)
		finish := func(e *Engine, j *Job) {
			defer wg.Done()
			zero := 0
			if err := e.Finish(j.LeaseID, Result{Outcome: "succeeded", ExitCode: &zero, Outputs: map[string]string{"tag": j.RunID}}); err != nil {
				t.Error(err)
			}
		}
		go finish(a, j1)
		go finish(b, j2)
		wg.Wait()
		s1, s2 := statuses(t, a, r1)["deploy-prod"], statuses(t, b, r2)["deploy-prod"]
		if !(s1 == "queued" && s2 == "waiting" || s1 == "waiting" && s2 == "queued") {
			t.Fatalf("round %d: deploy steps are %s and %s; exactly one should hold the lock", round, s1, s2)
		}
		if lockRows(t, a) != 1 {
			t.Fatalf("round %d: %d locks", round, lockRows(t, a))
		}
		// Finish the holder through one server; the other's reaper hands the
		// lock over.
		j := leaseDeploy(t, a, deployer)
		applied(t, b, j, "c")
		observe(t, e1(a, b, round), deployer, watchOf(t, a, deployer, "deploy-prod"), healthy(true))
		ok(t, a, take(t, a), nil) // smoke
		var waiter string
		if s1 == "waiting" {
			waiter = r1
		} else {
			waiter = r2
		}
		deadline := time.Now().Add(20 * time.Second)
		// Polled from the table alone: the other server's reaper may hand
		// the lock over between reading the tables and the events.
		for stepOf(t, a, waiter, "deploy-prod").Status != StepQueuedS {
			if time.Now().After(deadline) {
				t.Fatalf("round %d: the waiting run never got the lock", round)
			}
			time.Sleep(50 * time.Millisecond)
		}
		// Clear the second deployment through to its end.
		applied(t, a, leaseDeploy(t, b, deployer), "c")
		observe(t, a, deployer, watchOf(t, b, deployer, "deploy-prod"), healthy(true))
		ok(t, b, take(t, b), nil)
		if lockRows(t, a) != 0 {
			t.Fatalf("round %d: locks left", round)
		}
	}
}

func e1(a, b *Engine, round int) *Engine {
	if round%2 == 0 {
		return a
	}
	return b
}

// Validation of deploy steps, line by line.
func TestDeployValidation(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{strings.Replace(shopPipeline, "needs: build\n    environment", "environment", 1), "line 17: steps.deploy-prod: deploy-prod: tag refers to build.tag, but build is not in this step's needs"},
		{strings.Replace(shopPipeline, "deployment: deploy-prod", "deployment: smoke", 1), "deployment \"smoke\" is not a deploy step"},
		{strings.Replace(shopPipeline, "needs: [smoke]", "needs: [build]", 1), "rollback-prod: must need deploy-prod, directly or through other steps"},
	} {
		_, err := pipeline.Parse([]byte(tc.src))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("got %v\nwant %s", err, tc.want)
		}
	}
}

// A build limited to schema 10 (the release before deployments) against a
// database at schema 11 leaves deploy runs alone: it does not lease their
// steps, expire their waits, cancel or resume them, nor take their locks;
// a server of schema 11 finishes them. Its own runs go on as usual.
func TestOlderBuildLeavesDeployRunsAlone(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	addr := testdb.New(t)
	cur := openDeploy(t, addr, clk, prodEnv)
	id := createShop(t, cur, shopPipeline, 1)
	build(t, cur, id, "1")
	applied(t, cur, leaseDeploy(t, cur, deployer), "c1")
	if sum, _ := cur.Summary(id); sum.MinSchema != schemaDeploy {
		t.Fatalf("min_schema %d", sum.MinSchema)
	}

	var buf syncBuffer
	old := mustOpenAs(t, addr, clk, schemaDeploy-1, &buf)
	defer old.Close()
	d, err := old.GetRun(id)
	if err != nil {
		t.Fatal(err)
	}
	if !d.NeedsNewerServer || len(d.NotUnderstood) == 0 || !strings.Contains(strings.Join(d.NotUnderstood, ";"), "deploy") {
		t.Fatalf("older build's view: needs newer %v, not understood %v", d.NeedsNewerServer, d.NotUnderstood)
	}
	if err := old.Cancel(id, "ann"); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("older build canceled a deploy run: %v", err)
	}
	if _, err := old.CreateRun(NewRun{Project: "shop", Pipeline: parse(t, shopPipeline), Source: gitSource}); !errors.Is(err, ErrConflict) {
		t.Fatalf("older build created a deploy run: %v", err)
	}
	// An older server lists waiting deploy steps among its approvals (it
	// cannot tell them apart), but cannot decide them.
	if err := old.Decide(id, "deploy-prod", true, "ann", ""); !errors.Is(err, ErrNeedsNewer) {
		t.Fatalf("older build decided a deploy step: %v", err)
	}
	// Past the deadline, the older build's reaper leaves the step alone.
	clk.Add(11 * time.Minute)
	old.expireWaits()
	old.reap()
	if s := stepOf(t, cur, id, "deploy-prod"); s.Status != StepWaitingS {
		t.Fatalf("the older build changed the step: %s", s.Status)
	}
	if lockRows(t, cur) != 1 {
		t.Fatal("the lock is gone")
	}
	// Its own runs go on.
	own := create(t, old, linear, Trigger{Event: "manual"})
	for range 3 {
		j, err := old.Lease(context.Background(), RunnerInfo{ID: "old-runner", Name: "old"}, 0)
		if err != nil || j == nil {
			t.Fatalf("older build's lease: %v %v", j, err)
		}
		if j.RunID != own {
			t.Fatalf("older build leased %s/%s", j.RunID, j.StepID)
		}
		if _, err := old.Start(j.LeaseID); err != nil {
			t.Fatal(err)
		}
		ok(t, old, j, nil)
	}
	// The newer server ends the deploy step at its deadline.
	cur.expireWaits()
	want(t, statuses(t, cur, id), "deploy-prod", "failed", "rollback-prod", "queued")
}

// A deploy runner waiting for work is not an idle build runner: build
// runners holding runs still take new ones.
func TestDeployRunnersAreNotIdleBuildRunners(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	noDeploy(t, e, deployer) // the deploy runner is seen, and holds nothing
	createShop(t, e, shopPipeline, 1)
	createShop(t, e, shopPipeline, 2)
	take(t, e)
	take(t, e) // runner-1 already holds a lease; it still gets the second run
}

// A notification of a kind a server does not know is ignored: servers of
// schema 10 have no case for noteDeploy, and this server none for kinds a
// later release may add (handleNote's switch has no default).
func TestUnknownNotificationKindIsIgnored(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	ch, stop := e.Hub().Subscribe(func(hub.Message) bool { return true })
	defer stop()
	e.handleNote(note{Kind: "from-a-later-release", Run: "run_x"})
	select {
	case m := <-ch:
		t.Fatalf("published %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// Pods that keep crashing fail the deployment at once, with the pod's own last
// words as the reason, and the step's own log says what it saw and why it
// failed (the run page shows the step log and the failure reason).
func TestCrashLoopingPodsFailTheStepWithTheirOwnReason(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "2.0.0")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	s := healthy(true)
	s.App.Health, s.App.Sync = "Progressing", "Synced"
	s.Rollout = nil
	s.Evidence = []deploy.PodEvidence{{Pod: "shop-7d9-abc", Reason: "CrashLoopBackOff", Restarts: 4, Crash: true,
		LogTail: []string{"time=... level=INFO starting", "the configured root key does not open this database's encryption keys (key version 1 was wrapped by a key_file root key, and the one configured is not it). This server will not start"}}}
	r := observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), s)
	if r.Phase != deploy.PhaseCrashLoop || !strings.Contains(r.Detail, "new pods are crash-looping") || !strings.Contains(r.Detail, "does not open this database's encryption keys") {
		t.Fatalf("observation: %+v", r)
	}
	if v := stepOf(t, e, id, "deploy-prod"); !strings.Contains(v.Error, "crash-looping") {
		t.Fatalf("reason: %q", v.Error)
	}
	lines, _, err := e.Logs(id, "deploy-prod", 1, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var text []string
	for _, l := range lines {
		text = append(text, l.Text)
	}
	if all := strings.Join(text, "\n"); !strings.Contains(all, "[FAILED] crash_looping") || !strings.Contains(all, "encryption keys") {
		t.Fatalf("the step log lacks the failure:\n%s", all)
	}
}
