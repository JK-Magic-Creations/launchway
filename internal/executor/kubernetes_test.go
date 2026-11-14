package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These tests need a real cluster, for example:
//
//	kind create cluster --name launchway-test
//	kubectl --context kind-launchway-test create namespace launchway-test
//	LAUNCHWAY_TEST_KUBE_CONTEXT=kind-launchway-test go test ./internal/executor/ -run Kubernetes
func kube(t *testing.T) *Kubernetes {
	t.Helper()
	ctxName := os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT")
	if ctxName == "" {
		t.Skip("set LAUNCHWAY_TEST_KUBE_CONTEXT to run Kubernetes tests")
	}
	k, err := NewKubernetes(KubernetesOptions{Context: ctxName, Namespace: "launchway-test", StartTimeout: "5m"}, "test-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	return k
}

const alpine = "docker.io/library/alpine:3.22"

var twoTools = []ContainerSpec{{Name: "alpine", Image: alpine}, {Name: "busybox", Image: "docker.io/library/busybox:1.37"}}

func kspec(t *testing.T, runID, name, image, script string, ws string) Spec {
	container := "alpine"
	if image != alpine {
		container = "busybox"
	}
	return Spec{RunID: runID, Containers: twoTools, Container: container, Name: name, Image: image,
		Script: script, Env: map[string]string{"GREETING": "it's working"}, Workspace: ws, MetaDir: t.TempDir()}
}

func TestKubernetesRunSharesWorkspaceAcrossSteps(t *testing.T) {
	k := kube(t)
	ctx := context.Background()
	runID := fmt.Sprintf("run_kt%d", time.Now().UnixNano())
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "from-checkout.txt"), []byte("checked out\n"), 0o644)
	os.MkdirAll(filepath.Join(ws, "sub"), 0o755)
	os.Symlink("../from-checkout.txt", filepath.Join(ws, "sub", "link"))
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(runID, k.owner), metav1.DeleteOptions{})

	// Step 1, in alpine: reads the checkout, writes a file and an output.
	var c capture
	s1 := kspec(t, runID, "s1", alpine, `cat from-checkout.txt; cat sub/link
echo "$GREETING"
echo made-by-step-1 > shared.txt
echo answer=42 >> "$LAUNCHWAY_OUTPUTS"
echo to-stderr >&2`, ws)
	code, err := k.Run(ctx, s1, c.sink)
	if err != nil || code != 0 {
		t.Fatalf("step 1: code %d err %v\n%s", code, err, c.text())
	}
	for _, want := range []string{"stdout: checked out", "stdout: it's working", "stderr: to-stderr", "system: Starting pod"} {
		if !strings.Contains(c.text(), want) {
			t.Fatalf("missing %q in:\n%s", want, c.text())
		}
	}
	if got := ReadOutputs(s1.MetaDir)["answer"]; got != "42" {
		t.Fatalf("outputs %q", got)
	}

	// Step 2, in a different image in the same pod, sees step 1's file.
	var c2 capture
	code, err = k.Run(ctx, kspec(t, runID, "s2", "docker.io/library/busybox:1.37", "cat shared.txt; exit 7", ws), c2.sink)
	if err != nil || code != 7 {
		t.Fatalf("step 2: code %d err %v\n%s", code, err, c2.text())
	}
	if !strings.Contains(c2.text(), "made-by-step-1") || strings.Contains(c2.text(), "Starting pod") {
		t.Fatalf("step 2 should reuse the pod and see the file:\n%s", c2.text())
	}

	// Cleanup keeps the pod while the run is active, removes it once it succeeded.
	lookup := func(status string) RunLookup {
		return func(context.Context, []string) (map[string]RunInfo, error) {
			return map[string]RunInfo{runID: {ID: runID, Status: status, FinishedAt: time.Now().UnixMilli()}}, nil
		}
	}
	if err := k.Cleanup(ctx, lookup("running")); err != nil {
		t.Fatal(err)
	}
	if _, err := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, podName(runID, k.owner), metav1.GetOptions{}); err != nil {
		t.Fatalf("pod removed while the run was active: %v", err)
	}
	if err := k.Cleanup(ctx, lookup("succeeded")); err != nil {
		t.Fatal(err)
	}
	p, err := k.client.CoreV1().Pods(k.o.Namespace).Get(ctx, podName(runID, k.owner), metav1.GetOptions{})
	if err == nil && p.DeletionTimestamp == nil {
		t.Fatal("pod not removed after the run succeeded")
	}
}

func TestKubernetesCancel(t *testing.T) {
	k := kube(t)
	runID := fmt.Sprintf("run_kc%d", time.Now().UnixNano())
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(context.Background(), podName(runID, k.owner), metav1.DeleteOptions{})
	ws := t.TempDir()
	// Start the pod first so the timing below measures only the cancel.
	if _, err := k.Run(context.Background(), kspec(t, runID, "warm", alpine, "true", ws), func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err := k.Run(ctx, kspec(t, runID, "slow", alpine, "echo started; sleep 300", ws), func(string, string) {})
	if err == nil {
		t.Fatal("expected the context error")
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("took %s to stop", time.Since(start))
	}
}

func TestKubernetesBadImageIsExplained(t *testing.T) {
	k := kube(t)
	runID := fmt.Sprintf("run_kb%d", time.Now().UnixNano())
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(context.Background(), podName(runID, k.owner), metav1.DeleteOptions{})
	s := kspec(t, runID, "x", "docker.io/library/no-such-image-launchway:never", "true", t.TempDir())
	s.Containers = []ContainerSpec{{Name: "bad", Image: s.Image}}
	s.Container = "bad"
	var c capture
	_, err := k.Run(context.Background(), s, c.sink)
	if err == nil || !strings.Contains(err.Error(), "cannot pull docker.io/library/no-such-image-launchway:never") {
		t.Fatalf("err %v", err)
	}
}

// A service runs beside the steps and is reachable at localhost.
var webService = ContainerSpec{Name: "web", Image: "docker.io/library/nginx:1.29-alpine", Service: true}

const reachService = `for i in $(seq 1 30); do wget -q -O - http://localhost:80/ 2>/dev/null | grep -q nginx && { echo service reachable; exit 0; }; sleep 1; done; exit 1`

func TestKubernetesService(t *testing.T) {
	k := kube(t)
	runID := fmt.Sprintf("run_ks%d", time.Now().UnixNano())
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(context.Background(), podName(runID, k.owner), metav1.DeleteOptions{})
	s := kspec(t, runID, "s", alpine, reachService, t.TempDir())
	s.Containers = append(append([]ContainerSpec{}, twoTools...), webService)
	var c capture
	code, err := k.Run(context.Background(), s, c.sink)
	if err != nil || code != 0 || !strings.Contains(c.text(), "service reachable") {
		t.Fatalf("code %d err %v\n%s", code, err, c.text())
	}
	if !strings.Contains(c.text(), "web (service)") {
		t.Fatalf("the start message should list the service:\n%s", c.text())
	}
}
