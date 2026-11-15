package argocd

import (
	"context"
	"errors"
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/deploy"
	"github.com/jk-magic-creations/launchway/internal/engine"
)

// Commands on rollouts: promote, promote-full, abort and retry, each a
// status change, applied only while the rollout is still where the command
// expects it (deploy.Decide).

// Command applies a command. It returns the result for the server, or an
// error wrapping ErrUnavailable when the cluster cannot be reached (the
// runner then hands the command back).
func (a *Agent) Command(ctx context.Context, j *engine.CommandJob, log func(string, ...any)) (result, detail string, err error) {
	cl, t, err := a.cluster(j.Target)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch {
	case !t.AppAllowed(j.Project, j.Application):
		return deploy.NotApplied, fmt.Sprintf("this runner may not act on application %s for project %s", j.Application, j.Project), nil
	case !t.RolloutAllowed(j.Rollout.Namespace):
		return deploy.NotApplied, fmt.Sprintf("this runner may not act on rollouts in namespace %s (see deploy.targets.%s.rollout_namespaces)", j.Rollout.Namespace, j.Target), nil
	}
	return a.applyCommand(ctx, cl, j.Rollout.Namespace, j.Rollout.Name, j.Action, j.Expect, log)
}

func (a *Agent) applyCommand(ctx context.Context, cl *Cluster, ns, name, action string, x deploy.Expect, log func(string, ...any)) (string, string, error) {
	for tries := 0; ; tries++ {
		obj, err := cl.Rollout(ctx, ns, name)
		if err != nil {
			return "", "", fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		ro := deploy.ReduceRollout(obj, nil)
		d := deploy.Decide(action, x, ro)
		if !d.Apply {
			log("Rollout %s/%s: %s not applied: %s", ns, name, action, d.Detail)
			return d.Result, d.Detail, nil
		}
		meta := mapOf(obj, "metadata")
		rv, _ := meta["resourceVersion"].(string)
		err = cl.PatchRolloutStatus(ctx, ns, name, rv, deploy.StatusPatch(action))
		switch {
		case err == nil:
			detail := fmt.Sprintf("%s rollout %s/%s on %s", action, ns, name, x)
			log("Applied: %s", detail)
			return deploy.Applied, detail, nil
		case errors.Is(err, errConflict) && tries < 5:
			continue // the rollout changed: decide again
		case errors.Is(err, ErrNoStatusRight):
			return deploy.CommandFailed, err.Error() + " (grant patch on rollouts/status for this rollout)", nil
		case errors.Is(err, errConflict):
			return "", "", fmt.Errorf("%w: rollout %s/%s kept changing", ErrUnavailable, ns, name)
		default:
			return "", "", err
		}
	}
}

// abortFirst aborts a deployment's rollout before its revert, if the
// rollout is still on the deployment's pod template. It says what it did.
func (a *Agent) abortFirst(ctx context.Context, d *engine.DeployJob, log func(string, ...any)) (string, error) {
	if d.PodHash == "" {
		log("Not aborting rollout %s/%s first: this deployment's version was never seen in it", d.Rollout.Namespace, d.Rollout.Name)
		return "not aborted: the deployment's version was never seen in the rollout", nil
	}
	cl, t, err := a.cluster(d.Target)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !t.RolloutAllowed(d.Rollout.Namespace) {
		return fmt.Sprintf("not aborted: this runner may not act on rollouts in namespace %s", d.Rollout.Namespace), nil
	}
	log("Aborting rollout %s/%s first (abort_first), if it is still on %s", d.Rollout.Namespace, d.Rollout.Name, d.PodHash)
	result, detail, err := a.applyCommand(ctx, cl, d.Rollout.Namespace, d.Rollout.Name, deploy.Abort, deploy.Expect{PodHash: d.PodHash}, log)
	if err != nil {
		return "", err
	}
	if result == deploy.CommandFailed {
		// The revert is what counts; go on without the abort.
		log("Could not abort first (%s); reverting all the same", detail)
	}
	return result + ": " + detail, nil
}
