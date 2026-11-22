package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testdb"
)

// The step's time limit runs from the moment a deploy runner takes it. A step that waited longer than its limit for a
// runner (seen live: 38 minutes behind a runner that had hung) still gets all of its time once one takes it.
func TestDeployTimeLimitStartsWhenARunnerTakesTheStep(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: prodEnv,
		Queues: QueueSettings{DeployPickupWait: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")

	clk.Add(38 * time.Minute) // far past the step's own 10m
	e.expireWaits()
	want(t, statuses(t, e, id), "deploy-prod", "queued")

	j := leaseDeploy(t, e, deployer)
	applied(t, e, j, "c1")
	want(t, statuses(t, e, id), "deploy-prod", "waiting")

	clk.Add(9 * time.Minute) // 47 minutes after queueing, 9 after being taken
	e.expireWaits()
	want(t, statuses(t, e, id), "deploy-prod", "waiting")

	clk.Add(2 * time.Minute) // 11 after being taken
	e.expireWaits()
	want(t, statuses(t, e, id), "deploy-prod", "failed")
	if v := stepOf(t, e, id, "deploy-prod"); !strings.Contains(v.Error, "not healthy within 10m") {
		t.Fatalf("reason %q", v.Error)
	}
}

// The wait for a runner is its own, bounded by queues.deploy_pickup_wait, and ends with a reason that says nobody took
// the step, with the kind of failure that says so.
func TestNoDeployRunnerHasItsOwnBoundedWait(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: prodEnv,
		Queues: QueueSettings{DeployPickupWait: 3 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")

	clk.Add(2 * time.Minute)
	e.expireWaits()
	want(t, statuses(t, e, id), "deploy-prod", "queued")
	clk.Add(2 * time.Minute)
	e.expireWaits()
	want(t, statuses(t, e, id), "deploy-prod", "failed")
	v := stepOf(t, e, id, "deploy-prod")
	if !strings.Contains(v.Error, "took the step within 3m") || strings.Contains(v.Error, "ran out") {
		t.Fatalf("reason %q", v.Error)
	}
	if v.Cause == nil || v.Cause.Kind != CauseNoRunner {
		t.Fatalf("cause %+v", v.Cause)
	}
}

// "off" waits for a runner for ever: the step's own limit still does not run before one takes it.
func TestDeployPickupWaitOff(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	e, err := Open(Options{Database: testdb.New(t), LeaseTTL: ttl, Now: clk.Now, Environments: prodEnv,
		Queues: QueueSettings{DeployPickupWait: -1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	clk.Add(48 * time.Hour)
	e.expireWaits()
	want(t, statuses(t, e, id), "deploy-prod", "queued")
	applied(t, e, leaseDeploy(t, e, deployer), "c1")
	want(t, statuses(t, e, id), "deploy-prod", "waiting")
}

// A deploy step that waits for a deploy runner says so in plain words on the run's data: which pool serves the target and
// when a runner of it last asked for work, or that its runner is busy.
func TestQueuedDeployStepSaysWhatItWaitsFor(t *testing.T) {
	e, clk := newDeployEngine(t, prodEnv)
	deployer := deployer // as the API registers a pool token's runner
	deployer.Kind = PoolDeploy
	id := createShop(t, e, shopPipeline, 1)
	build(t, e, id, "1")
	v := stepOf(t, e, id, "deploy-prod")
	if v.WaitingCode != WaitNoRunner || !strings.Contains(v.WaitingFor, "waiting for a deploy runner serving target cluster-a: none has asked for work lately") {
		t.Fatalf("no runner known: %q (%s)", v.WaitingFor, v.WaitingCode)
	}

	e.touchRunner(deployer) // a poll: the runner is seen at 09:00
	clk.Add(5 * time.Minute)
	v = stepOf(t, e, id, "deploy-prod")
	if v.WaitingCode != WaitNoRunner || v.WaitingFor != "waiting for a deploy runner of pool deploy: none has asked for work since 09:00 UTC" {
		t.Fatalf("stale runner: %q (%s)", v.WaitingFor, v.WaitingCode)
	}

	// The runner polls again and takes the first deploy; a second service's deploy waits behind it.
	j := leaseDeploy(t, e, deployer)
	second := strings.Replace(shopPipeline, "environment: prod", "environment: prod\n    service: api", 1)
	id2 := createShop(t, e, second, 2)
	build(t, e, id2, "2")
	v = stepOf(t, e, id2, "deploy-prod")
	if v.WaitingCode != WaitRunnersBusy || v.WaitingFor != "waiting for a deploy runner of pool deploy: its runner is busy with 1 action" {
		t.Fatalf("busy runner: %q (%s)", v.WaitingFor, v.WaitingCode)
	}
	applied(t, e, j, "c1")
}
