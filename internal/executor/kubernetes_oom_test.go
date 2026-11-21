package executor

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestMemoryKillNote(t *testing.T) {
	lim := &corev1.Container{Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("6Gi")}}}
	oom := &corev1.ContainerStatus{LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}}}
	if got := memoryKillNote(oom, lim, 137); !strings.Contains(got, "used more memory than its limit (6Gi)") || !strings.Contains(got, "raise resources.memory_limit") {
		t.Fatalf("OOMKilled: %q", got)
	}
	if got := memoryKillNote(&corev1.ContainerStatus{}, lim, 137); !strings.Contains(got, "most likely because it used more memory than its limit (6Gi)") {
		t.Fatalf("bare 137 with a limit: %q", got)
	}
	if got := memoryKillNote(&corev1.ContainerStatus{}, &corev1.Container{}, 137); got != "" {
		t.Fatalf("137 with no memory limit says nothing: %q", got)
	}
	if got := memoryKillNote(oom, lim, 1); got == "" {
		t.Fatal("an OOMKilled container is reported whatever the code")
	}
	if got := memoryKillNote(&corev1.ContainerStatus{}, lim, 2); got != "" {
		t.Fatalf("an ordinary failure: %q", got)
	}
}
