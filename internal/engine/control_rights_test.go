package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/jk-magic-creations/launchway/internal/deploy"
)

// A deploy runner that reports, in its observation, that the cluster does
// not let it change the rollout's status makes the commands unavailable
// before anyone presses them, saying what an administrator must enable; a
// request is refused with the same words. Once it reports the right, they
// come back.
func TestCommandsUnavailableWhenRunnerMayNotControl(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, waitPipeline, 1)
	build(t, e, id, "2")
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	it := watchOf(t, e, deployer, "deploy-prod")
	snap := paused()
	snap.Rights = &deploy.Rights{Control: no(), ControlWhy: "an administrator must list the rollout in the chart value delivery.deployRunner.rbac.controlRollouts"}
	observe(t, e, deployer, it, snap)
	dep := onlyDep(t, e, id).ID

	_, ctls, err := e.Controls(id, dep, "alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range ctls {
		if c.Action == "retry" {
			continue // the rollout is not aborted: the rollout's state decides
		}
		if c.Available || !c.RunnerCannot || !strings.Contains(c.Reason, "rbac.controlRollouts") {
			t.Fatalf("%s must be unavailable with the reason: %+v", c.Action, c)
		}
	}
	_, err = e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote-full", Actor: "alice"})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "rbac.controlRollouts") {
		t.Fatalf("a request must be refused with the reason: %v", err)
	}

	snap.Rights = &deploy.Rights{Control: yes()}
	observe(t, e, deployer, it, snap)
	if _, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote", Actor: "alice"}); err != nil {
		t.Fatalf("with the right: %v", err)
	}
}

// A command the runner could not apply ends failed with the reason, and the
// command is readable that way from the deployment.
func TestFailedCommandKeepsItsReason(t *testing.T) {
	e, _ := newDeployEngine(t, prodEnv)
	id := createShop(t, e, waitPipeline, 1)
	build(t, e, id, "2")
	applied(t, e, leaseDeploy(t, e, deployer), "c2")
	observe(t, e, deployer, watchOf(t, e, deployer, "deploy-prod"), paused())
	dep := onlyDep(t, e, id).ID
	if _, err := e.RequestCommand(CommandRequest{Run: id, Deployment: dep, Action: "promote-full", Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	j := takeCommand(t, e)
	if err := e.FinishCommand(deployer, deployer.Targets, CommandResult{Take: j.Take, Result: deploy.CommandFailed, Detail: "the deploy runner may not change the rollout's status"}); err != nil {
		t.Fatal(err)
	}
	cv, err := e.CommandByTake(j.Take)
	if err != nil || cv.State != CommandDone || cv.Result != deploy.CommandFailed || !strings.Contains(cv.Detail, "may not change") {
		t.Fatalf("%+v %v", cv, err)
	}
}
