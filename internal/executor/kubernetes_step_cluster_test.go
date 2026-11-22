package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These tests run a pod for each step on a real cluster, as the tests in
// kubernetes_test.go do for a pod for each run (see there for how to make the
// cluster and its restricted namespace). They need the namespace to hold no
// ResourceQuota, which TestKubernetesStepQuota makes and removes itself.
func kubeStep(t *testing.T) *Kubernetes {
	t.Helper()
	ctxName := os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT")
	if ctxName == "" {
		t.Skip("set LAUNCHWAY_TEST_KUBE_CONTEXT to run Kubernetes tests")
	}
	k, err := NewKubernetes(KubernetesOptions{Context: ctxName, Namespace: kubeTestNamespace(), StartTimeout: "5m", Pod: PodPerStep, WorkDir: t.TempDir()}, "test-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	return k
}

func ownPods(t *testing.T, k *Kubernetes, run string) []corev1.Pod {
	t.Helper()
	list, err := k.client.CoreV1().Pods(k.o.Namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelRun + "=" + run})
	if err != nil {
		t.Fatal(err)
	}
	var out []corev1.Pod
	for _, p := range list.Items {
		if p.DeletionTimestamp == nil {
			out = append(out, p)
		}
	}
	return out
}

func kstep(t *testing.T, ws, run, id, image, memory, script string, more ...ContainerSpec) Spec {
	t.Helper()
	own := ContainerSpec{Name: "tool", Image: image, Memory: memory, MemoryLimit: memory, ExplicitMemory: memory != ""}
	return Spec{RunID: run, StepID: id, Attempt: 1, LeaseID: "l-" + id, Name: "launchway-l-" + id, Containers: append([]ContainerSpec{own}, more...),
		Container: "tool", Image: image, Script: script, Workspace: ws, WorkspaceID: "w1", MetaDir: t.TempDir()}
}

func TestKubernetesStepPods(t *testing.T) {
	k := kubeStep(t)
	ctx := context.Background()
	run := fmt.Sprintf("run_sp%d", time.Now().UnixNano())
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "README"), []byte("checked out\n"), 0o644)
	os.Symlink("README", filepath.Join(ws, "link"))
	busybox := "docker.io/library/busybox:1.37"

	var c1 capture
	s1 := kstep(t, ws, run, "one", alpine, "64Mi", `cat README; rm README; echo made > made.txt; mkdir -p reports; echo '<ok/>' > reports/a.xml
echo answer=42 >> "$LAUNCHWAY_OUTPUTS"; printf '#!/bin/sh\n' > run.sh; chmod +x run.sh`)
	code, err := k.Run(ctx, s1, c1.sink)
	if err != nil || code != 0 || !strings.Contains(c1.text(), "stdout: checked out") {
		t.Fatalf("step 1: %d %v\n%s", code, err, c1.text())
	}
	if got := ReadOutputs(s1.MetaDir)["answer"]; got != "42" {
		t.Fatalf("outputs %q", got)
	}
	if n := ownPods(t, k, run); len(n) != 0 {
		t.Fatalf("the step's pod is still there: %d", len(n))
	}
	// The next step, in another image in another pod, sees what the first left.
	var c2 capture
	s2 := kstep(t, ws, run, "two", busybox, "32Mi", `[ ! -e README ] && cat made.txt && [ -x run.sh ] && [ "$(readlink link)" = README ] && echo ok-two; exit 7`)
	code, err = k.Run(ctx, s2, c2.sink)
	if err != nil || code != 7 || !strings.Contains(c2.text(), "stdout: made") || !strings.Contains(c2.text(), "ok-two") {
		t.Fatalf("step 2: %d %v\n%s", code, err, c2.text())
	}
	got, err := k.CollectFiles(ctx, s2, []string{"reports/*.xml"}, CollectLimits{}.WithDefaults())
	if err != nil || len(got.Files) != 1 {
		t.Fatalf("collect: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "made.txt")); err == nil {
		t.Fatal("the checkout was written to")
	}

	// Two steps of one image at the same time: two pods, two limits.
	var wg sync.WaitGroup
	for _, st := range []struct{ id, mem string }{{"big", "96Mi"}, {"small", "48Mi"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := kstep(t, ws, run, st.id, alpine, st.mem, "sleep 8; echo "+st.id+" > "+st.id+".txt")
			if code, err := k.Run(ctx, s, func(string, string) {}); err != nil || code != 0 {
				t.Errorf("%s: %d %v", st.id, code, err)
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Minute)
	var seen []corev1.Pod
	for time.Now().Before(deadline) {
		if seen = ownPods(t, k, run); len(seen) == 2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if len(seen) != 2 {
		t.Fatalf("pods at the same time: %d", len(seen))
	}
	var limits []string
	for _, p := range seen {
		if len(p.Spec.Containers) != 2 {
			t.Errorf("%s has %d containers", p.Name, len(p.Spec.Containers))
		}
		for _, ct := range p.Spec.Containers {
			if ct.Name == "tool" {
				limits = append(limits, ct.Resources.Limits.Memory().String())
			}
		}
	}
	slices.Sort(limits)
	if !slices.Equal(limits, []string{"48Mi", "96Mi"}) {
		t.Fatalf("limits %v", limits)
	}
	wg.Wait()
	dir := k.WorkspaceDir(s1)
	for _, f := range []string{"big.txt", "small.txt", "made.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	if n := ownPods(t, k, run); len(n) != 0 {
		t.Fatalf("pods are left: %d", len(n))
	}
}

// A step killed for memory is explained after its pod is gone.
func TestKubernetesStepOOM(t *testing.T) {
	k := kubeStep(t)
	run := fmt.Sprintf("run_so%d", time.Now().UnixNano())
	s := kstep(t, t.TempDir(), run, "hog", alpine, "32Mi", `head -c 200000000 /dev/zero | tail -c 1 >/dev/null; x=$(head -c 100000000 /dev/zero | tr '\0' x); echo ${#x}`)
	var c capture
	code, err := k.Run(context.Background(), s, c.sink)
	if err != nil || code != 137 {
		t.Fatalf("%d %v\n%s", code, err, c.text())
	}
	if note := k.ExplainExit(context.Background(), s, code); !strings.Contains(note, "more memory than its limit (32Mi)") {
		t.Fatalf("note %q", note)
	}
	if n := ownPods(t, k, run); len(n) != 0 {
		t.Fatalf("pods are left: %d", len(n))
	}
}

// A service is in the step's pod and reachable at localhost.
func TestKubernetesStepPodService(t *testing.T) {
	k := kubeStep(t)
	run := fmt.Sprintf("run_ss%d", time.Now().UnixNano())
	s := kstep(t, t.TempDir(), run, "svc", alpine, "64Mi", reachKubeService, kubeWebService)
	var c capture
	code, err := k.Run(context.Background(), s, c.sink)
	if err != nil || code != 0 || !strings.Contains(c.text(), "served-by-uid-1000") {
		t.Fatalf("%d %v\n%s", code, err, c.text())
	}
}

// The namespace's quota on a real cluster: a step that can never fit fails at
// once; one that does not fit now waits and says who holds the room; a pod of
// the run's own that nothing runs in is released, so a retry never waits for it.
func TestKubernetesStepQuota(t *testing.T) {
	k := kubeStep(t)
	ctx := context.Background()
	rqs := k.client.CoreV1().ResourceQuotas(k.o.Namespace)
	if _, err := rqs.Create(ctx, &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "lw-steps-test"},
		Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{corev1.ResourceRequestsMemory: resource.MustParse("640Mi")}}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	defer rqs.Delete(ctx, "lw-steps-test", metav1.DeleteOptions{})
	run := fmt.Sprintf("run_sq%d", time.Now().UnixNano())
	ws := t.TempDir()
	waitQuota := func() { // the quota's own accounting is a moment behind
		time.Sleep(3 * time.Second)
	}

	// Can never fit: 1Gi against 640Mi, whatever else runs.
	code, err := k.Run(ctx, kstep(t, ws, run, "huge", alpine, "1Gi", "true"), func(string, string) {})
	if err == nil || code != -1 || !strings.Contains(err.Error(), "can never start") {
		t.Fatalf("huge: %d %v", code, err)
	}

	// Fits alone; does not fit beside a running step of the same run.
	hold := make(chan struct{})
	go func() {
		defer close(hold)
		k.Run(ctx, kstep(t, ws, run, "holder", alpine, "400Mi", "sleep 20"), func(string, string) {})
	}()
	deadline := time.Now().Add(2 * time.Minute)
	for len(ownPods(t, k, run)) == 0 && time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
	}
	waitQuota()
	code, err = k.Run(ctx, kstep(t, ws, run, "waiter", alpine, "400Mi", "true"), func(string, string) {})
	capErr, ok := AsCapacity(err)
	if !ok || capErr.Code != WaitQuotaFull || !strings.Contains(err.Error(), "this Sortie's steps holder hold") || !strings.Contains(err.Error(), "nothing ran out of memory") {
		t.Fatalf("waiter: %d %v", code, err)
	}
	t.Logf("what a waiting step says: %v", err)
	<-hold
	waitQuota()

	// An abandoned pod of this Station's own run holds 400Mi and no step runs in it.
	stray := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "launchway-stray-" + fmt.Sprint(time.Now().UnixNano()%100000), Namespace: k.o.Namespace,
		Labels:      map[string]string{labelManaged: "launchway", labelRun: run, labelOwner: k.owner, labelKind: "step"},
		Annotations: map[string]string{annoStep: "gone", annoAttempt: "1"}},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr(true), RunAsUser: ptr(int64(1000)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			TerminationGracePeriodSeconds: ptr(int64(0)),
			Containers: []corev1.Container{{Name: "x", Image: alpine, Command: []string{"sleep", "600"}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("400Mi")}, Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("400Mi")}}}}}}
	if _, err := k.client.CoreV1().Pods(k.o.Namespace).Create(ctx, stray, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitQuota()
	var c capture
	code, err = k.Run(ctx, kstep(t, ws, run, "retry", alpine, "400Mi", "echo ran"), c.sink)
	if err != nil || code != 0 || !strings.Contains(c.text(), "Released pod "+stray.Name) {
		t.Fatalf("retry: %d %v\n%s", code, err, c.text())
	}
}
