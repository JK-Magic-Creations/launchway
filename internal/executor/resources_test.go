package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func q(s string) resource.Quantity { return resource.MustParse(s) }

// What a step asked for is what the pod asks of the cluster: a step that
// declares 6 GiB gets 6 GiB, as a request and as a limit.
func TestKubePodGivesAContainerWhatItWasGiven(t *testing.T) {
	k := testKube(t, KubernetesOptions{})
	for _, c := range []struct {
		name                           string
		sp                             ContainerSpec
		cpuReq, cpuLim, memReq, memLim string // "" means not set
	}{
		{"declared 6Gi and 4 CPUs", ContainerSpec{Name: "go", Image: "golang", CPU: "4", CPULimit: "4", Memory: "6Gi", MemoryLimit: "6Gi"},
			"4", "4", "6Gi", "6Gi"},
		{"the default: promised less than it may use", ContainerSpec{Name: "go", Image: "golang", CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "6Gi"},
			"1", "4", "2Gi", "6Gi"},
		{"memory asked for without a limit is capped at it, as before", ContainerSpec{Name: "go", Image: "golang", Memory: "1Gi"},
			"50m", "", "1Gi", "1Gi"},
		{"cpu asked for without a limit has none, as before", ContainerSpec{Name: "go", Image: "golang", CPU: "500m"},
			"500m", "", "64Mi", ""},
		{"nothing asked for: the runner's own settings", ContainerSpec{Name: "go", Image: "golang"},
			"50m", "", "64Mi", ""},
		{"a request above the limit is brought down to it", ContainerSpec{Name: "go", Image: "golang", CPULimit: "20m"},
			"20m", "20m", "64Mi", ""},
	} {
		pod, _ := k.podSpec("p", "run_1", []ContainerSpec{c.sp})
		got := pod.Spec.Containers[1].Resources
		check := func(what string, list corev1.ResourceList, name corev1.ResourceName, want string) {
			g, ok := list[name]
			switch {
			case want == "" && ok:
				t.Errorf("%s: %s %s is %s, want none", c.name, what, name, g.String())
			case want != "" && (!ok || g.Cmp(q(want)) != 0):
				t.Errorf("%s: %s %s is %v, want %s", c.name, what, name, g.String(), want)
			}
		}
		check("request", got.Requests, corev1.ResourceCPU, c.cpuReq)
		check("limit", got.Limits, corev1.ResourceCPU, c.cpuLim)
		check("request", got.Requests, corev1.ResourceMemory, c.memReq)
		check("limit", got.Limits, corev1.ResourceMemory, c.memLim)
	}
}

// The runner's own memory limit for containers that ask for nothing still
// applies, as before.
func TestKubePodKeepsTheRunnersOwnMemoryLimit(t *testing.T) {
	k := testKube(t, KubernetesOptions{Memory: "128Mi", MemoryLimit: "1Gi"})
	pod, _ := k.podSpec("p", "run_1", []ContainerSpec{{Name: "go", Image: "golang"}})
	r := pod.Spec.Containers[1].Resources
	if r.Requests.Memory().Cmp(q("128Mi")) != 0 || r.Limits.Memory().Cmp(q("1Gi")) != 0 {
		t.Fatalf("%+v", r)
	}
}

// The failure this work is for: a container that may use 2 GiB but sees 32
// CPUs. The tools in it are told how many parallel jobs fit, through the
// step's environment, and what the step sets itself is left alone.
func TestParallelismFollowsTheMemoryGranted(t *testing.T) {
	s := Spec{Container: "go", Env: map[string]string{"GREETING": "hi"},
		Containers: []ContainerSpec{{Name: "go", Image: "golang", CPULimit: "32", Memory: "2Gi", MemoryLimit: "2Gi", Threads: 2}}}
	got := s.withParallelism().Env
	for k, want := range map[string]string{"GOMAXPROCS": "2", "MAKEFLAGS": "-j2", "CARGO_BUILD_JOBS": "2", "CMAKE_BUILD_PARALLEL_LEVEL": "2",
		"UV_THREADPOOL_SIZE": "2", "LAUNCHWAY_PARALLELISM": "2", "GREETING": "hi"} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if len(s.Env) != 1 {
		t.Errorf("the spec's own environment was changed: %v", s.Env)
	}
	// A step that sets GOMAXPROCS itself keeps its value.
	s.Env = map[string]string{"GOMAXPROCS": "1"}
	if got := s.withParallelism().Env; got["GOMAXPROCS"] != "1" || got["MAKEFLAGS"] != "-j2" {
		t.Errorf("%v", got)
	}
	// No opinion, no variables: a step that was not sized behaves as before.
	s = Spec{Container: "go", Containers: []ContainerSpec{{Name: "go", Image: "golang"}}}
	if got := s.withParallelism().Env; len(got) != 0 {
		t.Errorf("%v", got)
	}
	// A service is not a step.
	s = Spec{Container: "db", Containers: []ContainerSpec{{Name: "db", Service: true, Threads: 4}}}
	if got := s.withParallelism().Env; len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestContainerFlags(t *testing.T) {
	got := strings.Join(containerFlags(ContainerSpec{CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "6Gi"}), " ")
	want := "--memory 6442450944 --memory-swap 6442450944 --memory-reservation 2147483648 --cpus 4"
	if got != want {
		t.Errorf("%s\nwant %s", got, want)
	}
	if got := strings.Join(containerFlags(ContainerSpec{Memory: "512Mi"}), " "); got != "--memory 536870912 --memory-swap 536870912" {
		t.Errorf("memory asked for without a limit is also the cap: %s", got)
	}
	if got := containerFlags(ContainerSpec{CPU: "500m"}); len(got) != 0 {
		t.Errorf("a CPU asked for without a limit is no limit: %v", got)
	}
	if got := containerFlags(ContainerSpec{}); len(got) != 0 {
		t.Errorf("%v", got)
	}
	for milli, want := range map[int64]string{4000: "4", 500: "0.5", 2250: "2.25", 1: "0.001"} {
		if got := cpusFlag(milli); got != want {
			t.Errorf("cpusFlag(%d) = %s, want %s", milli, got, want)
		}
	}
}

// fakeContainerTool is a stand-in for podman or docker that records how it
// was called and what environment it was given.
func fakeContainerTool(t *testing.T) (bin, record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "record")
	bin = filepath.Join(dir, "podman")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + record + "\nenv | grep -E '^(GOMAXPROCS|MAKEFLAGS|GREETING)=' | sort >> " + record + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, record
}

// Podman and Docker apply the limits as flags of the step's own container,
// and the tools in it are told how many jobs fit.
func TestContainerExecutorAppliesTheLimitsAndParallelism(t *testing.T) {
	bin, record := fakeContainerTool(t)
	c := &Container{Bin: bin}
	var out capture
	s := spec(t, "true")
	s.Container = "go"
	s.Containers = []ContainerSpec{{Name: "go", Image: s.Image, CPU: "1", CPULimit: "4", Memory: "6Gi", MemoryLimit: "6Gi", Threads: 4, Explicit: true}}
	if code, err := c.Run(context.Background(), s, out.sink); err != nil || code != 0 {
		t.Fatalf("code %d err %v\n%s", code, err, out.text())
	}
	b, _ := os.ReadFile(record)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	call := lines[0]
	for _, want := range []string{"--memory 6442450944", "--memory-swap 6442450944", "--cpus 4", " -e GOMAXPROCS ", " -e MAKEFLAGS "} {
		if !strings.Contains(call, want) {
			t.Errorf("podman was not called with %q:\n%s", want, call)
		}
	}
	if strings.Contains(call, "--memory-reservation") {
		t.Errorf("promised the same as the limit needs no reservation: %s", call)
	}
	if got := strings.Join(lines[1:], " "); !strings.Contains(got, "GOMAXPROCS=4") || !strings.Contains(got, "MAKEFLAGS=-j4") || !strings.Contains(got, "GREETING=hello world") {
		t.Errorf("environment: %s", got)
	}
	if !strings.Contains(out.text(), "Container resources: cpu 1 (up to 4), memory 6Gi; tools are told to run 4 parallel jobs") {
		t.Errorf("the log does not say what the container was given:\n%s", out.text())
	}
	// Flags go before the image; the image's command is the script.
	if i, j := strings.Index(call, "--memory "), strings.Index(call, s.Image); i < 0 || j < i {
		t.Errorf("flags must come before the image: %s", call)
	}

	// A step that was not sized is run as before: no flags.
	s.Containers = []ContainerSpec{{Name: "go", Image: s.Image}}
	if _, err := c.Run(context.Background(), s, out.sink); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(record)
	if call := strings.Split(string(b), "\n")[0]; strings.Contains(call, "--memory") || strings.Contains(call, "--cpus") || strings.Contains(call, "GOMAXPROCS") {
		t.Errorf("an unsized step must not be limited: %s", call)
	}
}

// With a real container runtime: the limit is really there, and a process
// that is told the parallelism sees it. Opt-in, like the other container tests.
func TestContainerExecutorLimitsAreReal(t *testing.T) {
	if testing.Short() || os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	c, err := DetectContainer()
	if err != nil {
		t.Skip(err)
	}
	if err := exec.Command(c.Bin, "pull", "-q", "docker.io/library/alpine:3.22").Run(); err != nil {
		t.Skipf("cannot pull alpine: %v", err)
	}
	var out capture
	s := spec(t, "echo gomaxprocs=$GOMAXPROCS\ncat /sys/fs/cgroup/memory.max 2>/dev/null || cat /sys/fs/cgroup/memory/memory.limit_in_bytes")
	s.Container = "go"
	s.Containers = []ContainerSpec{{Name: "go", Image: s.Image, Memory: "256Mi", MemoryLimit: "256Mi", Threads: 2, Explicit: true}}
	if code, err := c.Run(context.Background(), s, out.sink); err != nil || code != 0 {
		t.Fatalf("code %d err %v\n%s", code, err, out.text())
	}
	if !strings.Contains(out.text(), "gomaxprocs=2") || !strings.Contains(out.text(), "268435456") {
		t.Fatalf("the container was not limited:\n%s", out.text())
	}
}

// The host executor can limit nothing, so it refuses a step that asks, and
// says why; a step that asks for nothing runs as before.
func TestHostRefusesAStepThatAsksForResources(t *testing.T) {
	var out capture
	s := spec(t, "echo ran")
	s.Container = "go"
	s.Containers = []ContainerSpec{{Name: "go", Image: s.Image, CPU: "4", CPULimit: "4", Memory: "6Gi", MemoryLimit: "6Gi", Threads: 4, Explicit: true}}
	code, err := (&Host{}).Run(context.Background(), s, out.sink)
	if err == nil || code != -1 || errors.Is(err, ErrInfrastructure) {
		t.Fatalf("code %d err %v: a refusal is a failure of the step, not of the machinery", code, err)
	}
	for _, want := range []string{"host executor cannot limit CPU or memory", "cpu 4, memory 6Gi", "podman, docker or kubernetes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not say %q: %v", want, err)
		}
	}
	if strings.Contains(out.text(), "ran") {
		t.Fatal("the script ran")
	}

	// Defaults are not a request, and the host applies none of them.
	s.Containers[0].Explicit = false
	s.Script = "echo \"ran gomaxprocs=${GOMAXPROCS:-unset}\""
	if code, err := (&Host{}).Run(context.Background(), s, out.sink); err != nil || code != 0 {
		t.Fatalf("code %d err %v", code, err)
	}
	if !strings.Contains(out.text(), "ran gomaxprocs=unset") {
		t.Fatalf("the host must not apply limits it cannot enforce:\n%s", out.text())
	}
}

// ---- the namespace's own limits ----

func kubeWith(o KubernetesOptions, objs ...runtime.Object) *Kubernetes {
	o.Namespace = "launchway-steps"
	if o.HelperImage == "" {
		o.HelperImage = "busybox"
	}
	if o.CPU == "" {
		o.CPU = "50m"
	}
	if o.Memory == "" {
		o.Memory = "64Mi"
	}
	return &Kubernetes{o: o, client: fake.NewSimpleClientset(objs...)}
}

func limitRange(name string, items ...corev1.LimitRangeItem) *corev1.LimitRange {
	return &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "launchway-steps"}, Spec: corev1.LimitRangeSpec{Limits: items}}
}

func quota(name string, hard, used corev1.ResourceList) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "launchway-steps"},
		Status: corev1.ResourceQuotaStatus{Hard: hard, Used: used}}
}

var build = func(mem string) []ContainerSpec {
	return []ContainerSpec{{Name: "image-1", Image: "golang:1.27", CPU: "1", CPULimit: "4", Memory: mem, MemoryLimit: mem, ExplicitCPU: true, ExplicitMemory: true}}
}

func noSys(string, ...any) {}

// A step that asks for more than the namespace allows fails before its pod
// is made, with a message that names the limit and where it comes from.
func TestStepAskingForMoreThanTheNamespaceAllowsFailsNamingTheLimit(t *testing.T) {
	lr := limitRange("container-limits", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer,
		Max: corev1.ResourceList{corev1.ResourceMemory: q("8Gi")}, Min: corev1.ResourceList{corev1.ResourceMemory: q("16Mi")}})
	k := kubeWith(KubernetesOptions{}, lr)
	if _, err := k.checkResources(context.Background(), build("8Gi"), noSys); err != nil {
		t.Fatalf("at the limit is allowed: %v", err)
	}
	_, err := k.checkResources(context.Background(), build("12Gi"), noSys)
	if err == nil || errors.Is(err, ErrInfrastructure) {
		t.Fatalf("want a failure of the step, got %v", err)
	}
	for _, want := range []string{"container image-1 (golang:1.27)", "asks for memory", "12Gi", "above the 8Gi", `LimitRange "container-limits"`, "namespace launchway-steps", "per container"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not say %q: %v", want, err)
		}
	}
	// A pod of containers that are each allowed may still be too much for a pod limit.
	pod := limitRange("pod-limits", corev1.LimitRangeItem{Type: corev1.LimitTypePod, Max: corev1.ResourceList{corev1.ResourceMemory: q("10Gi")}})
	k = kubeWith(KubernetesOptions{}, pod)
	two := []ContainerSpec{{Name: "a", Image: "x", Memory: "6Gi", ExplicitMemory: true}, {Name: "b", Image: "y", Memory: "6Gi", ExplicitMemory: true}}
	if _, err := k.checkResources(context.Background(), two, noSys); err == nil || !strings.Contains(err.Error(), `LimitRange "pod-limits"`) || !strings.Contains(err.Error(), "per pod") {
		t.Fatalf("%v", err)
	}
	// Below a minimum.
	lr = limitRange("floor", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer, Min: corev1.ResourceList{corev1.ResourceMemory: q("1Gi")}})
	k = kubeWith(KubernetesOptions{}, lr)
	if _, err := k.checkResources(context.Background(), []ContainerSpec{{Name: "a", Image: "x", Memory: "512Mi", ExplicitMemory: true}}, noSys); err == nil || !strings.Contains(err.Error(), "below the 1Gi") {
		t.Fatalf("%v", err)
	}
}

func TestPodAskingForMoreThanTheQuotaHoldsFails(t *testing.T) {
	hard := corev1.ResourceList{corev1.ResourceLimitsMemory: q("32Gi"), corev1.ResourceRequestsMemory: q("64Gi")}
	k := kubeWith(KubernetesOptions{}, quota("compute", hard, corev1.ResourceList{corev1.ResourceLimitsMemory: q("0"), corev1.ResourceRequestsMemory: q("0")}))
	if _, err := k.checkResources(context.Background(), build("6Gi"), noSys); err != nil {
		t.Fatalf("%v", err)
	}
	// Six containers of 6 GiB cannot be in a 32 GiB namespace at all.
	var six []ContainerSpec
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		six = append(six, ContainerSpec{Name: n, Image: n, Memory: "6Gi", ExplicitMemory: true})
	}
	_, err := k.checkResources(context.Background(), six, noSys)
	if err == nil || errors.Is(err, ErrInfrastructure) {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"limits.memory", "36Gi", "32Gi", `ResourceQuota "compute"`, "namespace launchway-steps", "no pod of this size can run there"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not say %q: %v", want, err)
		}
	}
	// When it fits but other pods are using the quota, it is tried again later.
	k = kubeWith(KubernetesOptions{}, quota("compute", hard, corev1.ResourceList{corev1.ResourceLimitsMemory: q("30Gi"), corev1.ResourceRequestsMemory: q("1Gi")}))
	_, err = k.checkResources(context.Background(), build("6Gi"), noSys)
	if !errors.Is(err, ErrInfrastructure) || !strings.Contains(err.Error(), "30Gi is in use by other pods") {
		t.Fatalf("%v", err)
	}
}

func TestRunnersOwnMaximumsAreCheckedAndNamed(t *testing.T) {
	k := kubeWith(KubernetesOptions{MaxMemory: "8Gi", MaxCPU: "4"})
	if _, err := k.checkResources(context.Background(), build("8Gi"), noSys); err != nil {
		t.Fatal(err)
	}
	_, err := k.checkResources(context.Background(), build("12Gi"), noSys)
	if err == nil || !strings.Contains(err.Error(), "kubernetes.max_memory 8Gi in this runner's configuration") || !strings.Contains(err.Error(), "12Gi") {
		t.Fatalf("%v", err)
	}
	big := []ContainerSpec{{Name: "a", Image: "x", CPU: "8", CPULimit: "8", Memory: "1Gi", ExplicitCPU: true, ExplicitMemory: true}}
	if _, err := k.checkResources(context.Background(), big, noSys); err == nil || !strings.Contains(err.Error(), "kubernetes.max_cpu 4") {
		t.Fatalf("%v", err)
	}
}

// A runner that may not read LimitRanges or quotas says so once and carries
// on; the cluster has the last word. Any other failure to read is retried.
func TestUnreadableNamespaceLimitsAreSaidOnceNotFatal(t *testing.T) {
	k := kubeWith(KubernetesOptions{})
	cs := k.client.(*fake.Clientset)
	forbidden := func(resource string) {
		cs.PrependReactor("list", resource, func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: resource}, "", errors.New("no"))
		})
	}
	forbidden("limitranges")
	forbidden("resourcequotas")
	var said []string
	sys := func(f string, a ...any) { said = append(said, f) }
	for i := 0; i < 3; i++ {
		if _, err := k.checkResources(context.Background(), build("6Gi"), sys); err != nil {
			t.Fatal(err)
		}
	}
	if len(said) != 2 || !strings.Contains(said[0], "LimitRanges") || !strings.Contains(said[1], "ResourceQuotas") {
		t.Fatalf("said %q", said)
	}
	// A server that is down is not a reason to run without the check.
	k = kubeWith(KubernetesOptions{})
	k.client.(*fake.Clientset).PrependReactor("list", "limitranges", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	if _, err := k.checkResources(context.Background(), build("6Gi"), noSys); !errors.Is(err, ErrInfrastructure) {
		t.Fatalf("%v", err)
	}
}

// The check is made when the pod is made, before anything is created.
func TestEnsurePodRefusesBeforeCreatingAnything(t *testing.T) {
	lr := limitRange("container-limits", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer, Max: corev1.ResourceList{corev1.ResourceMemory: q("8Gi")}})
	k := kubeWith(KubernetesOptions{}, lr)
	k.pods = map[string]*runPod{}
	k.owner = "owner"
	s := Spec{RunID: "run_1", Container: "image-1", Containers: build("12Gi"), WorkspaceID: "w"}
	var out capture
	_, err := k.ensurePod(context.Background(), s, out.sink)
	if err == nil || !strings.Contains(err.Error(), `LimitRange "container-limits"`) {
		t.Fatalf("%v", err)
	}
	pods, _ := k.client.CoreV1().Pods("launchway-steps").List(context.Background(), metav1.ListOptions{})
	if len(pods.Items) != 0 {
		t.Fatalf("a pod was created: %d", len(pods.Items))
	}
}

// What the server chose on its own, the installation's defaults, never
// fails a step: it is lowered to the runner's maximum and the namespace's
// LimitRange, with one line saying so. What the step asked for still fails.
func TestServerDefaultsAreLoweredToWhatTheRunnerAndNamespaceAllow(t *testing.T) {
	lr := limitRange("container-limits", corev1.LimitRangeItem{Type: corev1.LimitTypeContainer,
		Max: corev1.ResourceList{corev1.ResourceMemory: q("4Gi")}})
	k := kubeWith(KubernetesOptions{MaxCPU: "2", MaxMemory: "8Gi"}, lr)
	// The recommended default: 1 CPU and 2Gi promised, 4 CPUs and 6Gi at most.
	def := []ContainerSpec{{Name: "image-1", Image: "golang:1.27", CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "6Gi"}}
	var said []string
	sys := func(f string, a ...any) { said = append(said, fmt.Sprintf(f, a...)) }
	got, err := k.checkResources(context.Background(), def, sys)
	if err != nil {
		t.Fatalf("a default failed the step: %v", err)
	}
	if got[0].CPULimit != "2" || got[0].MemoryLimit != "4Gi" || got[0].CPU != "1" || got[0].Memory != "2Gi" {
		t.Fatalf("not lowered: %+v", got[0])
	}
	if def[0].CPULimit != "4" || def[0].MemoryLimit != "6Gi" {
		t.Fatalf("the caller's spec was changed: %+v", def[0])
	}
	if len(said) != 1 || !strings.Contains(said[0], "lowered") || !strings.Contains(said[0], "cpu_limit 4 to 2, kubernetes.max_cpu") ||
		!strings.Contains(said[0], `memory_limit 6Gi to 4Gi, LimitRange "container-limits"`) {
		t.Fatalf("said %q", said)
	}
	// What a step asked for is held to the same limits and fails.
	asked := []ContainerSpec{{Name: "image-1", Image: "golang:1.27", CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "6Gi",
		ExplicitCPU: true, ExplicitMemory: true}}
	if _, err := k.checkResources(context.Background(), asked, noSys); err == nil || !strings.Contains(err.Error(), "container image-1") {
		t.Fatalf("an explicit ask above the limits must fail: %v", err)
	}
	// A step that asked for memory only: its memory is held, its CPU default is lowered.
	mixed := []ContainerSpec{{Name: "image-1", Image: "golang:1.27", CPU: "1", CPULimit: "4", Memory: "2Gi", MemoryLimit: "2Gi", ExplicitMemory: true}}
	got, err = k.checkResources(context.Background(), mixed, noSys)
	if err != nil || got[0].CPULimit != "2" || got[0].MemoryLimit != "2Gi" {
		t.Fatalf("%+v %v", got, err)
	}
}

// With nothing sized by the server, the runner's own memory and memory_limit
// settings apply as they did before step resources.
func TestOperatorsMemoryLimitStillAppliesWhenTheServerSendsNoMemory(t *testing.T) {
	k := kubeWith(KubernetesOptions{Memory: "256Mi", MemoryLimit: "1Gi"})
	for _, sp := range []ContainerSpec{{Name: "a", Image: "x"}, {Name: "a", Image: "x", CPU: "2", CPULimit: "4"}} {
		res := k.resources(sp)
		if m := res.Requests[resMemory]; m.String() != "256Mi" {
			t.Errorf("%+v: memory request %s", sp, m.String())
		}
		if m, ok := res.Limits[resMemory]; !ok || m.String() != "1Gi" {
			t.Errorf("%+v: the runner's memory_limit was not applied: %v", sp, res.Limits)
		}
	}
	// The server's memory, if it sends any, is what the container gets.
	res := k.resources(ContainerSpec{Name: "a", Image: "x", Memory: "2Gi", MemoryLimit: "6Gi"})
	if m := res.Limits[resMemory]; m.String() != "6Gi" {
		t.Errorf("memory limit %s", m.String())
	}
	// A server that cleared its defaults and left a step to the namespace
	// passes the pod check as it did before.
	if _, err := k.checkResources(context.Background(), []ContainerSpec{{Name: "a", Image: "x"}}, noSys); err != nil {
		t.Fatal(err)
	}
}
