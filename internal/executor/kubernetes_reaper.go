package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The reaper: what keeps a run's step pod from outliving the run. See
// docs/reference/runner-config.md, "What happens to a run's pod".

// holdRun marks the run as executing a step in this process, so that the
// reaper leaves its pod alone, and returns what ends the mark. The mark is
// on the run, not on the pod object, which ensurePod may replace.
func (k *Kubernetes) holdRun(runID string) func() {
	k.mu.Lock()
	if k.busy == nil {
		k.busy = map[string]int{}
	}
	k.busy[runID]++
	k.mu.Unlock()
	return func() {
		k.mu.Lock()
		if k.busy[runID]--; k.busy[runID] <= 0 {
			delete(k.busy, runID)
		}
		k.mu.Unlock()
	}
}

// Cleanup is the reaper. It deletes the step pods in the namespace that no
// longer need to exist, whichever runner made them: a runner that was
// replaced, for example by a Deployment, leaves its pods to the others. A
// pod goes when
//   - the server says its run finished (see removable; a failed or canceled
//     run's pod stays only for keep_failed, 0 unless set),
//   - the server does not know its run and this runner made it,
//   - it is older than max_pod_age and no step of its run is executing in
//     this process, whatever the server says (a crashed runner's pod, a run
//     nobody holds a lease on, a run of another installation).
//
// A pod with a step executing here is never removed.
func (k *Kubernetes) Cleanup(ctx context.Context, lookup RunLookup) error {
	sel := fmt.Sprintf("%s=launchway,%s", labelManaged, labelRun)
	list, err := k.client.CoreV1().Pods(k.o.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil || len(list.Items) == 0 {
		return err
	}
	ids := make([]string, 0, len(list.Items))
	for _, p := range list.Items {
		ids = append(ids, p.Labels[labelRun])
	}
	infos, err := lookup(ctx, ids)
	if err != nil {
		return err
	}
	k.mu.Lock()
	busy := make(map[string]bool, len(k.busy))
	for id := range k.busy {
		busy[id] = true
	}
	k.mu.Unlock()
	for _, p := range list.Items {
		id := p.Labels[labelRun]
		if busy[id] || p.DeletionTimestamp != nil {
			continue
		}
		info, known := infos[id]
		mine := p.Labels[labelOwner] == k.owner
		expired := k.maxPodAge > 0 && time.Since(p.CreationTimestamp.Time) > k.maxPodAge
		if !removable(info, known, mine, k.keepFailed) && !expired {
			continue
		}
		if err := k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if mine {
			k.forget(id)
		}
	}
	return nil
}

// createFailed is the error for a pod the API server would not create.
func createFailed(pod string, err error) error {
	if isQuotaError(err) {
		return quotaFull(pod, err)
	}
	return cannotStart("creating pod %s: %v", pod, err)
}

// isQuotaError says the API server refused a pod because the namespace's
// ResourceQuota is used up.
func isQuotaError(err error) bool {
	return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "exceeded quota")
}

// quotaFull is the error for a step that cannot start because the step
// namespace's quota is used up. A quota that frees up when other runs end is
// a wait for room (see capacity.go), not a failure; a pod that asks for more
// than the whole quota is a failure, at once.
func quotaFull(pod string, err error) error { return quotaWait(pod, err) }
