package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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

// ReleaseRun deletes this runner's pod of the run, unless a step of the run is
// executing in this process. The runner calls it when the server has taken a
// step away (its lease was lost): the retry may be given to another runner,
// and the pod of the abandoned attempt would hold the step namespace's quota
// until the run ended. A retry that comes back to this runner makes the pod
// again, which costs a start-up but never a stuck wait.
func (k *Kubernetes) ReleaseRun(ctx context.Context, runID string) error {
	k.mu.Lock()
	if k.busy[runID] > 0 {
		k.mu.Unlock()
		return nil
	}
	k.mu.Unlock()
	err := k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(runID, k.owner), metav1.DeleteOptions{GracePeriodSeconds: ptr(int64(0))})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	k.forget(runID)
	return nil
}

// orphaned says that the pod is one this runner made in an earlier life: it
// is this runner's, it is older than this process, and this process has
// never used it. The steps that used it were running under leases that the
// server has since taken back, whatever the run's status says, so it only
// holds quota. (A runner keeps its identity across restarts, so "mine"
// includes the pods of its previous process.)
func (k *Kubernetes) orphaned(p *corev1.Pod, mine bool) bool {
	if !mine || k.started.IsZero() || !p.CreationTimestamp.Time.Before(k.started) {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.pods[p.Labels[labelRun]] == nil
}

// Cleanup is the reaper. It deletes the step pods in the namespace that no
// longer need to exist, whichever runner made them: a runner that was
// replaced, for example by a Deployment, leaves its pods to the others. A
// pod goes when
//   - the server says its run finished (see removable; a failed or canceled
//     run's pod stays only for keep_failed, 0 unless set),
//   - the server does not know its run and this runner made it,
//   - this runner made it in an earlier process and has not used it since
//     (see orphaned): a restarted runner holds no step of it,
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
		if !removable(info, known, mine, k.keepFailed) && !expired && !k.orphaned(&p, mine) {
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
func (k *Kubernetes) createFailed(ctx context.Context, pod, runID string, err error) error {
	if isQuotaError(err) {
		return quotaFull(pod, err, k.holdOfRun(ctx, runID))
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
func quotaFull(pod string, err error, held *runHold) error { return quotaWait(pod, err, held) }

// holdOfRun is what all the pods of the run in the namespace have reserved,
// whichever runner made them; nil if they cannot be listed.
func (k *Kubernetes) holdOfRun(ctx context.Context, runID string) *runHold {
	list, err := k.client.CoreV1().Pods(k.o.Namespace).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("%s=launchway,%s=%s", labelManaged, labelRun, runID)})
	if err != nil {
		return nil
	}
	return holdOf(list.Items)
}
