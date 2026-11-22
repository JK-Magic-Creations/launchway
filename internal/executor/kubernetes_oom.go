package executor

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExitExplainer is implemented by executors that can say why a step's process
// ended with a bare exit code (killed for memory, for example). The runner
// puts the answer on the step instead of "exit code 137".
type ExitExplainer interface {
	ExplainExit(ctx context.Context, s Spec, code int) string
}

// memoryKillNote says in plain words that a step was killed because it used
// more memory than its limit; "" when nothing points that way. Kubernetes
// marks the container OOMKilled when its main process is the one killed; a
// step runs beside that process, so often only exit code 137 (SIGKILL) tells,
// and then the limit is what makes memory the likely reason.
func memoryKillNote(st *corev1.ContainerStatus, spec *corev1.Container, code int) string {
	oom := false
	if st != nil {
		if t := st.State.Terminated; t != nil && t.Reason == "OOMKilled" {
			oom = true
		}
		if t := st.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
			oom = true
		}
	}
	limit := ""
	if spec != nil {
		if q, ok := spec.Resources.Limits[corev1.ResourceMemory]; ok {
			limit = q.String()
		}
	}
	switch {
	case oom && limit != "":
		return fmt.Sprintf("the step was killed because it used more memory than its limit (%s); raise resources.memory_limit or reduce parallelism", limit)
	case oom:
		return "the step was killed because it used more memory than its container may use; raise resources.memory_limit or reduce parallelism"
	case code == 137 && limit != "":
		return fmt.Sprintf("the step was killed (exit code 137), most likely because it used more memory than its limit (%s); raise resources.memory_limit or reduce parallelism", limit)
	}
	return ""
}

// ExplainExit looks at the step's container in the run's pod.
func (k *Kubernetes) ExplainExit(ctx context.Context, s Spec, code int) string {
	if code != 137 {
		return ""
	}
	if k.perStep() {
		return k.takeNote(s.Name) // the step's pod is gone; the note was made before it was deleted
	}
	k.mu.Lock()
	rp := k.pods[s.RunID]
	k.mu.Unlock()
	if rp == nil {
		return ""
	}
	pod, err := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, rp.name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	name := rp.containers[s.Container]
	var st *corev1.ContainerStatus
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			st = &pod.Status.ContainerStatuses[i]
		}
	}
	var spec *corev1.Container
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			spec = &pod.Spec.Containers[i]
		}
	}
	return memoryKillNote(st, spec, code)
}
