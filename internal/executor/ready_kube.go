package executor

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// waitForServices is called while a pod is not ready yet: it says in the step
// log which services are waited for, and fails, naming the check and the tail
// of the service's output, when one has been running longer than its check
// allows.
func (k *Kubernetes) waitForServices(ctx context.Context, pod *corev1.Pod, specs []ContainerSpec, sys func(string, ...any), since map[string]time.Time, noted map[string]bool) error {
	ready := map[string]*Ready{}
	for _, sp := range specs {
		if sp.Service && sp.Ready != nil {
			ready[sp.Name] = sp.Ready
		}
	}
	running := map[string]bool{}
	for _, st := range pod.Status.ContainerStatuses {
		running[st.Name] = st.State.Running != nil
	}
	now := time.Now()
	for _, name := range unreadyServices(pod) {
		r := ready[name]
		if r == nil || !running[name] {
			continue
		}
		if _, ok := since[name]; !ok {
			since[name] = now
			sys("Waiting for service %s... (%s)", name, nonEmpty(r.Described, "its readiness probe"))
		}
		if now.Sub(since[name]) > r.Within+r.every() {
			return cannotStart("the service %s was not ready within %s (waiting for %s): %s", name, r.Within, nonEmpty(r.Described, "its readiness probe"), k.tail(ctx, pod.Name, name))
		}
	}
	return nil
}

// noteReady says in the step log how long each service that was waited for
// took.
func (k *Kubernetes) noteReady(sys func(string, ...any), since map[string]time.Time, noted map[string]bool) {
	for name, t := range since {
		if !noted[name] {
			noted[name] = true
			sys("%s", fmt.Sprintf("Service %s ready after %d s", name, int(time.Since(t).Seconds())))
		}
	}
}

// tail returns the last lines a container printed, for explaining failures.
func (k *Kubernetes) tail(ctx context.Context, pod, container string) string {
	n := int64(15)
	b, err := k.client.CoreV1().Pods(k.o.Namespace).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: &n}).DoRaw(ctx)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return "no output"
	}
	return strings.TrimSpace(string(b))
}

func allReady(p *corev1.Pod) bool {
	if len(p.Status.ContainerStatuses) != len(p.Spec.Containers) {
		return false
	}
	for _, st := range p.Status.ContainerStatuses {
		if st.State.Running == nil {
			return false
		}
	}
	return len(unreadyServices(p)) == 0
}
