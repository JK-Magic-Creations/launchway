package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// When the server takes a step away, the runner's pod of that run goes at
// once, so that the retry (here or on another runner) never waits for quota
// the abandoned pod holds. A pod with a step of the run executing is left.
func TestReleaseRunDeletesTheAbandonedPod(t *testing.T) {
	k := kubeWith(KubernetesOptions{})
	k.owner = "me"
	ctx := context.Background()
	run := "run_lost"
	for _, o := range []string{"me", "other"} {
		if _, err := k.client.CoreV1().Pods("launchway-steps").Create(ctx, runPodObj(k, run, o, time.Minute), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	k.pods = map[string]*runPod{run: {name: podName(run, "me"), ready: true}}

	release := k.holdRun(run)
	if err := k.ReleaseRun(ctx, run); err != nil || !has(t, k, run, "me") {
		t.Fatalf("a pod with a step executing was released (err %v)", err)
	}
	release()
	if err := k.ReleaseRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if has(t, k, run, "me") {
		t.Fatal("the abandoned pod is still there")
	}
	if !has(t, k, run, "other") {
		t.Fatal("another runner's pod of the run was removed")
	}
	if k.pods[run] != nil {
		t.Fatal("the executor still thinks it has the pod, so the retry would use a pod that is gone")
	}
	if err := k.ReleaseRun(ctx, run); err != nil {
		t.Fatalf("releasing twice failed: %v", err)
	}
}

// A runner that restarted removes the pods its earlier process made for runs
// that are still active (their steps' leases are gone), but not a pod this
// process uses, not one made after it started, and not another runner's.
func TestCleanupRemovesOrphansOfAnEarlierProcess(t *testing.T) {
	k := kubeWith(KubernetesOptions{})
	k.owner = "me"
	k.maxPodAge = 24 * time.Hour
	k.started = time.Now().Add(-time.Minute)
	ctx := context.Background()
	for _, p := range []struct {
		run, owner string
		age        time.Duration
	}{{"run_old", "me", time.Hour}, {"run_used", "me", time.Hour}, {"run_new", "me", time.Second}, {"run_foreign", "other", time.Hour}} {
		if _, err := k.client.CoreV1().Pods("launchway-steps").Create(ctx, runPodObj(k, p.run, p.owner, p.age), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	k.pods = map[string]*runPod{"run_used": {name: podName("run_used", "me"), ready: true}}
	say := serverSays(RunInfo{ID: "run_old", Status: "running"}, RunInfo{ID: "run_used", Status: "running"},
		RunInfo{ID: "run_new", Status: "running"}, RunInfo{ID: "run_foreign", Status: "running"})
	if err := k.Cleanup(ctx, say); err != nil {
		t.Fatal(err)
	}
	for run, want := range map[string]bool{"run_old": false, "run_used": true, "run_new": true} {
		if got := has(t, k, run, "me"); got != want {
			t.Errorf("%s: pod present %v, want %v", run, got, want)
		}
	}
	if !has(t, k, "run_foreign", "other") {
		t.Error("another runner's pod was removed")
	}
}

// The production case: a run with a pod of its own already in the namespace
// asks for a second one, and the quota is full. The sentence says what the
// run has reserved and across how many pods, what the namespace allows, and
// what must be released; it never reads as a step that ran out of memory.
func TestQuotaWaitSaysWhatTheRunHoldsAndWhatItWaitsFor(t *testing.T) {
	k := kubeWith(KubernetesOptions{})
	k.owner = "me"
	k.startWait = time.Second
	k.pods = map[string]*runPod{}
	if err := k.o.setDefaults(); err != nil {
		t.Fatal(err)
	}
	mem := func(q string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(q)}}
	}
	for _, name := range []string{"a", "b"} {
		p := runPodObj(k, "run_q", name, time.Minute)
		p.Spec.Containers = []corev1.Container{{Name: "node", Resources: mem("4Gi")}, {Name: "golang", Resources: mem("4Gi")}, {Name: "w", Resources: mem("256Mi")}}
		p.Status.Phase = corev1.PodRunning
		if _, err := k.client.CoreV1().Pods("launchway-steps").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	// A finished pod holds nothing.
	done := runPodObj(k, "run_q", "c", time.Minute)
	done.Spec.Containers = []corev1.Container{{Name: "x", Resources: mem("8Gi")}}
	done.Status.Phase = corev1.PodSucceeded
	k.client.CoreV1().Pods("launchway-steps").Create(context.Background(), done, metav1.CreateOptions{})

	cs := k.client.(*fake.Clientset)
	cs.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "launchway-run-r",
			errors.New("exceeded quota: steps, requested: requests.memory=8448Mi, used: requests.memory=16Gi, limited: requests.memory=16Gi"))
	})

	_, err := k.Run(context.Background(), quotaSpec(), func(string, string) {})
	if _, ok := AsCapacity(err); !ok {
		t.Fatalf("want a wait for room, got %v", err)
	}
	for _, want := range []string{
		"this run has reserved 16896Mi memory across 2 pods;", // 2 x (4Gi + 4Gi + 256Mi)
		"the namespace allows 16Gi memory", "16Gi memory is reserved in all",
		"needs 8448Mi memory more", "8448Mi memory must be released first", "nothing ran out of memory",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "already requested") {
		t.Error("the old wording is back")
	}
}
