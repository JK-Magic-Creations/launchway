package executor

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestReadinessProbe(t *testing.T) {
	r := &Ready{Run: "pg_isready -U postgres", Every: 2 * time.Second, Within: 60 * time.Second}
	p := readinessProbe(r)
	if p.Exec == nil || p.Exec.Command[2] != "pg_isready -U postgres" || p.PeriodSeconds != 2 || p.FailureThreshold != 30 {
		t.Fatalf("%+v", p)
	}
	if p := readinessProbe(&Ready{TCP: 6379, Every: 5 * time.Second, Within: 10 * time.Second}); p.TCPSocket == nil || p.TCPSocket.Port.IntValue() != 6379 || p.FailureThreshold != 2 {
		t.Fatalf("%+v", p)
	}
	if p := readinessProbe(&Ready{HTTPPort: 8080, HTTPPath: "/health", Every: time.Second, Within: time.Second}); p.HTTPGet == nil || p.HTTPGet.Path != "/health" || p.FailureThreshold != 1 {
		t.Fatalf("%+v", p)
	}
	if readinessProbe(nil) != nil {
		t.Fatal("no check, no probe")
	}
}

func TestPodmanHealthArgs(t *testing.T) {
	args := strings.Join(podmanHealthArgs(&Ready{Run: "pg_isready -U 'x'", Every: 2 * time.Second, Within: 20 * time.Second}), " ")
	for _, want := range []string{"--health-cmd sh -c 'pg_isready -U '\\''x'\\'''", "--health-interval 2s", "--health-retries 10"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q in %s", want, args)
		}
	}
	if cmd := healthCommand(&Ready{TCP: 6379}); !strings.Contains(cmd, "nc -z 127.0.0.1 6379") || !strings.Contains(cmd, "/dev/tcp/127.0.0.1/6379") {
		t.Fatal(cmd)
	}
	if cmd := healthCommand(&Ready{HTTPPort: 80, HTTPPath: "/up"}); !strings.Contains(cmd, "wget -q -O /dev/null http://127.0.0.1:80/up") {
		t.Fatal(cmd)
	}
	if podmanHealthArgs(nil) != nil {
		t.Fatal("no check, no arguments")
	}
}

// A pod is ready only when the containers with a probe are ready too.
func TestAllReadyWaitsForProbes(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "launchway-helper"}, {Name: "db", ReadinessProbe: &corev1.Probe{}}}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "launchway-helper", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			{Name: "db", Ready: false, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}},
	}
	if allReady(pod) || len(unreadyServices(pod)) != 1 {
		t.Fatal("db is not ready")
	}
	pod.Status.ContainerStatuses[1].Ready = true
	if !allReady(pod) {
		t.Fatal("db is ready")
	}
}
