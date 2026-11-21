package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// A pod whose image cannot be pulled, or that stopped, is reported as a
// failure to start, with the text people already know, so that the runner can
// tell the server it is infrastructure and not the step's.
func TestWaitRunningFailuresCannotStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		pod  *corev1.Pod
		text string
	}{
		{"an image that cannot be pulled", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "c", Image: "alpine:nope", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull", Message: "manifest unknown"}}}}}},
			"cannot pull alpine:nope: manifest unknown"},
		{"a pod that stopped", &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed}}, "the pod stopped: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.pod.Name, tc.pod.Namespace = "p", "launchway-steps"
			k := kubeWith(KubernetesOptions{}, runtime.Object(tc.pod))
			k.startWait = time.Second
			err := k.waitRunning(context.Background(), "p", []ContainerSpec{{Name: "c"}}, func(string, ...any) {})
			if !errors.Is(err, ErrCannotStart) || errors.Is(err, ErrInfrastructure) || !strings.HasPrefix(err.Error(), tc.text) {
				t.Fatalf("%v", err)
			}
		})
	}
}
