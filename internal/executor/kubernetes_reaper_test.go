package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// runPodObj is a step pod of a run, as the executor makes it.
func runPodObj(k *Kubernetes, runID, owner string, age time.Duration) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: podName(runID, owner), Namespace: "launchway-steps",
		CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		Labels:            map[string]string{labelManaged: "launchway", labelRun: runID, labelOwner: owner},
	}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "main", Image: "docker.io/library/alpine:3.22"}}}}
}

func has(t *testing.T, k *Kubernetes, runID, owner string) bool {
	t.Helper()
	_, err := k.client.CoreV1().Pods("launchway-steps").Get(context.Background(), podName(runID, owner), metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func serverSays(infos ...RunInfo) RunLookup {
	return func(_ context.Context, ids []string) (map[string]RunInfo, error) {
		m := map[string]RunInfo{}
		for _, i := range infos {
			m[i.ID] = i
		}
		return m, nil
	}
}

// Every way a run can end releases its pod: the run succeeded, failed (at an
// image step or any other, with the later steps skipped), was canceled, timed
// out (the server marks it failed), or the server forgot it. The pod does not
// wait for a grace period unless the operator set one.
func TestReaperRemovesThePodOfEveryTerminalRun(t *testing.T) {
	now := time.Now().UnixMilli()
	for _, tc := range []struct {
		name string
		info *RunInfo // nil: the server does not know the run
	}{
		{"succeeded", &RunInfo{Status: "succeeded", FinishedAt: now}},
		{"failed at an image step, later steps skipped", &RunInfo{Status: "failed", FinishedAt: now}},
		{"failed at a command step after retries", &RunInfo{Status: "failed", FinishedAt: now}},
		{"failed by its timeout", &RunInfo{Status: "failed", FinishedAt: now}},
		{"canceled", &RunInfo{Status: "canceled", FinishedAt: now}},
		{"unknown to the server", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := kubeWith(KubernetesOptions{})
			k.owner = "me"
			ctx := context.Background()
			run := "run_t1"
			if _, err := k.client.CoreV1().Pods("launchway-steps").Create(ctx, runPodObj(k, run, "me", time.Minute), metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			// While a step of the run executes, the pod stays whatever the server says.
			release := k.holdRun(run)
			say := serverSays()
			if tc.info != nil {
				tc.info.ID = run
				say = serverSays(*tc.info)
			}
			if err := k.Cleanup(ctx, say); err != nil || !has(t, k, run, "me") {
				t.Fatalf("a pod with a step executing was removed (err %v)", err)
			}
			release()
			if err := k.Cleanup(ctx, say); err != nil {
				t.Fatal(err)
			}
			if has(t, k, run, "me") {
				t.Fatal("the pod of an ended run is still there")
			}
		})
	}
}

// A run still active keeps its pod (between steps, waiting for an approval);
// a grace period, when set, delays a failed run's pod; a run the server does
// not know belongs to someone else unless this runner made the pod.
func TestReaperKeepsWhatIsStillNeeded(t *testing.T) {
	now := time.Now().UnixMilli()
	k := kubeWith(KubernetesOptions{KeepFailed: "1h"})
	k.keepFailed = time.Hour
	k.maxPodAge = 24 * time.Hour
	k.owner = "me"
	ctx := context.Background()
	for _, p := range []*corev1.Pod{
		runPodObj(k, "run_active", "me", time.Minute),
		runPodObj(k, "run_grace", "me", time.Minute),
		runPodObj(k, "run_foreign", "other", time.Minute),
	} {
		if _, err := k.client.CoreV1().Pods("launchway-steps").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	say := serverSays(RunInfo{ID: "run_active", Status: "running"}, RunInfo{ID: "run_grace", Status: "failed", FinishedAt: now})
	if err := k.Cleanup(ctx, say); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"run_active", "run_grace"} {
		if !has(t, k, id, "me") {
			t.Errorf("%s: removed too early", id)
		}
	}
	if !has(t, k, "run_foreign", "other") {
		t.Error("the unknown run of another runner was removed")
	}
}

// A runner that crashed, or a run nobody holds a lease on, cannot leak a
// pod for longer than max_pod_age, even when the server still calls the run
// active or does not know it.
func TestReaperRemovesOldPodsWithNoLiveStep(t *testing.T) {
	k := kubeWith(KubernetesOptions{})
	k.maxPodAge = 24 * time.Hour
	k.owner = "me"
	ctx := context.Background()
	old, young := 25*time.Hour, time.Hour
	for _, p := range []*corev1.Pod{
		runPodObj(k, "run_crashed", "dead-runner", old), // active run, its runner crashed
		runPodObj(k, "run_other", "other", old),         // unknown run of another owner
		runPodObj(k, "run_young", "dead-runner", young), // same, but not old yet
		runPodObj(k, "run_longstep", "me", old),         // a step is executing here now
	} {
		if _, err := k.client.CoreV1().Pods("launchway-steps").Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	defer k.holdRun("run_longstep")()
	say := serverSays(RunInfo{ID: "run_crashed", Status: "running"}, RunInfo{ID: "run_young", Status: "running"}, RunInfo{ID: "run_longstep", Status: "running"})
	if err := k.Cleanup(ctx, say); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][2]any{
		"run_crashed": {"dead-runner", false}, "run_other": {"other", false},
		"run_young": {"dead-runner", true}, "run_longstep": {"me", true},
	} {
		if got := has(t, k, id, want[0].(string)); got != want[1].(bool) {
			t.Errorf("%s: pod present %v, want %v", id, got, want[1])
		}
	}
}

// Without a server answer nothing is removed on the strength of a guess,
// except by age.
func TestReaperStopsWhenTheServerCannotBeAsked(t *testing.T) {
	k := kubeWith(KubernetesOptions{})
	k.owner = "me"
	k.client.CoreV1().Pods("launchway-steps").Create(context.Background(), runPodObj(k, "run_x", "me", time.Minute), metav1.CreateOptions{})
	err := k.Cleanup(context.Background(), func(context.Context, []string) (map[string]RunInfo, error) { return nil, errors.New("down") })
	if err == nil || !has(t, k, "run_x", "me") {
		t.Fatalf("err %v", err)
	}
}

// A step that cannot start because the namespace's quota is used up waits
// for room (it is not a failure): the error says so in plain words, with the
// numbers, and carries a stable code.
func TestQuotaFullIsAWaitForRoom(t *testing.T) {
	k := quotaRefusing(t, "exceeded quota: steps, requested: requests.cpu=500m, used: requests.cpu=7800m, limited: requests.cpu=8")
	_, err := k.Run(context.Background(), quotaSpec(), func(string, string) {})
	c, ok := AsCapacity(err)
	if !ok || !errors.Is(err, ErrCapacity) || errors.Is(err, ErrCannotStart) || c.Code != WaitQuotaFull {
		t.Fatalf("want a wait for room, got %v", err)
	}
	for _, want := range []string{"waiting for room in the step namespace", "the namespace allows 8 CPU", "7.8 CPU is reserved in all", "needs 0.5 CPU more", "0.3 CPU must be released first", "nothing ran out of memory"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
	if k.busy["run_q"] != 0 {
		t.Fatal("the run is still marked as executing a step")
	}
}

// A single step that asks for more than the whole quota can never start: it
// fails at once, with both numbers and what to change.
func TestStepLargerThanTheWholeQuotaFailsAtOnce(t *testing.T) {
	k := quotaRefusing(t, "exceeded quota: steps, requested: requests.cpu=10, used: requests.cpu=0, limited: requests.cpu=8")
	_, err := k.Run(context.Background(), quotaSpec(), func(string, string) {})
	if !errors.Is(err, ErrCannotStart) || errors.Is(err, ErrCapacity) {
		t.Fatalf("want a start failure, got %v", err)
	}
	for _, want := range []string{"10 CPU", "8 CPU", "can never start", "raise the quota"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}

func quotaSpec() Spec {
	return Spec{RunID: "run_q", WorkspaceID: "w", Workspace: "/nonexistent", Container: "c",
		Containers: []ContainerSpec{{Name: "c", Image: "busybox"}}}
}

func quotaRefusing(t *testing.T, text string) *Kubernetes {
	t.Helper()
	k := kubeWith(KubernetesOptions{Pod: PodPerRun})
	k.owner = "me"
	k.startWait = time.Second
	k.pods = map[string]*runPod{}
	if err := k.o.setDefaults(); err != nil {
		t.Fatal(err)
	}
	cs := k.client.(*fake.Clientset)
	cs.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "launchway-run-r", errors.New(text))
	})
	return k
}
