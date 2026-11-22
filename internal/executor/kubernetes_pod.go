package executor

import (
	"context"
	"fmt"
	"io"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// podGone turns an error talking to the pod into an infrastructure error,
// forgetting the pod if it no longer exists so the next step recreates it.
func (k *Kubernetes) podGone(ctx context.Context, runID string, rp *runPod, err error) error {
	if _, gerr := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, rp.name, metav1.GetOptions{}); apierrors.IsNotFound(gerr) {
		k.forget(runID)
		return infra("the run's pod %s disappeared", rp.name)
	}
	return infra("talking to pod %s: %v", rp.name, err)
}

func (k *Kubernetes) forget(runID string) {
	k.mu.Lock()
	delete(k.pods, runID)
	k.mu.Unlock()
}

func (k *Kubernetes) signal(pod, container, dir, sig string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := fmt.Sprintf(`p=$(cat %s/pid 2>/dev/null) || exit 0; kill -%s -"$p" 2>/dev/null || kill -%s "$p" 2>/dev/null; true`, dir, sig, sig)
	_ = k.exec(ctx, pod, container, []string{"sh", "-c", cmd}, nil, io.Discard, io.Discard)
}
