package executor

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// stepCluster is a fake cluster that is as strict as the API server is about
// a pod (see newFakeAPI), enforces a ResourceQuota on the memory the pods
// request, brings every pod it admits up at once, and runs what a Station
// runs in a pod (the helper's tar, the step's script) as processes of the
// test on a directory for each pod, so that whole steps can be run, and the
// pods that exist at any moment can be looked at.
type stepCluster struct {
	t   testing.TB
	k   *Kubernetes
	cs  *fake.Clientset
	dir string

	mu      sync.Mutex
	hard    int64 // the namespace's quota on requests.memory, in bytes; 0: none
	created []corev1.Pod
	execs   []execRecord
	waiting map[string]string // image → why its pod does not start
}

type execRecord struct{ pod, container string }

const stepNS = "launchway-steps"

var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func newStepCluster(t testing.TB, o KubernetesOptions) *stepCluster {
	t.Helper()
	o.Pod = PodPerStep
	o.WorkDir = t.TempDir()
	c := &stepCluster{t: t, dir: t.TempDir(), waiting: map[string]string{}}
	c.k = kubeWith(o)
	if err := c.k.o.setDefaults(); err != nil {
		t.Fatal(err)
	}
	c.k.owner = "me"
	c.k.startWait = 2 * time.Second
	c.k.started = time.Now()
	c.cs = newFakeAPI()
	c.k.client = c.cs
	c.cs.PrependReactor("create", "pods", c.admit)
	c.k.runIn = c.runIn
	return c
}

// sibling is a second Station process of the same runner (as after a restart)
// using the same cluster.
func (c *stepCluster) sibling() *Kubernetes {
	k := kubeWith(c.k.o)
	k.o.setDefaults()
	k.owner = c.k.owner
	k.startWait = c.k.startWait
	k.client = c.cs
	k.runIn = c.runIn
	k.started = time.Now().Add(time.Second) // the earlier process's pods are older than this one
	k.maxPodAge = 24 * time.Hour
	return k
}

func (c *stepCluster) quota(memory string) {
	q := resource.MustParse(memory)
	c.hard = q.Value()
}

func podMemory(p *corev1.Pod) int64 {
	var n int64
	for _, ct := range p.Spec.Containers {
		n += ct.Resources.Requests.Memory().Value()
	}
	return n
}

// admit is the API server: the quota, and bringing the pod up.
func (c *stepCluster) admit(a ktesting.Action) (bool, runtime.Object, error) {
	pod := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hard > 0 {
		var used int64
		list, _ := c.cs.Tracker().List(corev1.SchemeGroupVersion.WithResource("pods"), corev1.SchemeGroupVersion.WithKind("Pod"), stepNS)
		for _, p := range list.(*corev1.PodList).Items {
			used += podMemory(&p)
		}
		if want := podMemory(pod); used+want > c.hard {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, pod.Name,
				fmt.Errorf("exceeded quota: steps, requested: requests.memory=%s, used: requests.memory=%s, limited: requests.memory=%s", mebi(want), mebi(used), mebi(c.hard)))
		}
	}
	if pod.CreationTimestamp.IsZero() {
		pod.CreationTimestamp = metav1.Now()
	}
	pod.Namespace = stepNS
	pod.Status.Phase = corev1.PodRunning
	for _, ct := range pod.Spec.Containers {
		st := corev1.ContainerStatus{Name: ct.Name, Image: ct.Image, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
		if why, ok := c.waiting[ct.Image]; ok {
			st.Ready = false
			st.State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: why, Message: "pulling " + ct.Image + " failed"}}
			pod.Status.Phase = corev1.PodPending
		}
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, st)
	}
	c.created = append(c.created, *pod.DeepCopy())
	return false, nil, nil
}

// exitError is what a command that ended with a status gives.
type exitError struct{ code int }

func (e exitError) Error() string   { return fmt.Sprintf("command terminated with exit code %d", e.code) }
func (e exitError) String() string  { return e.Error() }
func (e exitError) Exited() bool    { return true }
func (e exitError) ExitStatus() int { return e.code }

// runIn runs what the Station asks a pod's container to run, in a directory
// of its own that stands for the pod.
func (c *stepCluster) runIn(ctx context.Context, pod, container string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
	c.mu.Lock()
	c.execs = append(c.execs, execRecord{pod, container})
	c.mu.Unlock()
	root := filepath.Join(c.dir, pod)
	for _, d := range []string{"workspace", "launchway", "home"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o777)
	}
	args := make([]string, len(cmd))
	for i, a := range cmd {
		a = strings.ReplaceAll(a, "/home/launchway", filepath.Join(root, "home"))
		a = strings.ReplaceAll(a, "/workspace", filepath.Join(root, "workspace"))
		args[i] = strings.ReplaceAll(a, "/launchway/", filepath.Join(root, "launchway")+"/")
	}
	x := exec.CommandContext(ctx, args[0], args[1:]...)
	x.Stdin, x.Stdout, x.Stderr = stdin, stdout, stderr
	x.Cancel = func() error { return x.Process.Kill() }
	x.WaitDelay = time.Second
	err := x.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return exitError{ee.ExitCode()}
	}
	return err
}

// pods lists the pods that exist now.
func (c *stepCluster) pods() []corev1.Pod {
	c.t.Helper()
	list, err := c.cs.CoreV1().Pods(stepNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		c.t.Fatal(err)
	}
	return list.Items
}

func (c *stepCluster) podNames() []string {
	var out []string
	for _, p := range c.pods() {
		out = append(out, p.Name)
	}
	slices.Sort(out)
	return out
}

func (c *stepCluster) noPods() {
	c.t.Helper()
	if n := c.podNames(); len(n) > 0 {
		c.t.Fatalf("pods are left: %v", n)
	}
}

// waitFor waits until cond is true.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 600; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// checkout makes a directory like a run's checkout.
func checkout(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		// Files of a checkout are older than a step by more than a second.
		old := time.Now().Add(-time.Hour)
		_ = os.Chtimes(p, old, old)
	}
	return dir
}

// step makes the Spec of one step attempt. The container is the step's own,
// with its own memory.
func stepSpec(t testing.TB, ws, runID, id, memory, script string, services ...ContainerSpec) Spec {
	t.Helper()
	own := ContainerSpec{Name: "toolbox", Image: "docker.io/library/golang:1.27", Memory: memory, MemoryLimit: memory, ExplicitMemory: true}
	return Spec{RunID: runID, StepID: id, Attempt: 1, LeaseID: "lease-" + id, Name: "launchway-lease-" + id,
		Containers: append([]ContainerSpec{own}, services...), Container: "toolbox", Image: own.Image,
		Script: script, Workspace: ws, WorkspaceID: "ws1", MetaDir: t.TempDir()}
}

type lines struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lines) sink(stream, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.WriteString(stream + ": " + text + "\n")
}
func (l *lines) text() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func (c *stepCluster) run(s Spec) (int, string, error) {
	var l lines
	code, err := c.k.Run(context.Background(), s, l.sink)
	return code, l.text(), err
}

func readFile(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

// Acceptance case 1. Two steps of one stage that use one image used to run in
// one container with one memory limit, and the kernel killed both. Each now
// has a pod, and a container, of its own, with the limits of its own.
func TestTwoStepsOfOneImageAtTheSameTimeHavePodsAndLimitsOfTheirOwn(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"go.mod": "module x\n"})
	hold := filepath.Join(t.TempDir(), "go")
	wait := fmt.Sprintf("while [ ! -e %s ]; do sleep 0.02; done", hold)
	a := stepSpec(t, ws, "run_1", "test", "8Gi", wait+"; echo a > from-a.txt")
	b := stepSpec(t, ws, "run_1", "security-go", "1Gi", wait+"; echo b > from-b.txt")
	var wg sync.WaitGroup
	for _, s := range []Spec{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, text, err := c.run(s); err != nil || code != 0 {
				t.Errorf("%s: code %d err %v\n%s", s.StepID, code, err, text)
			}
		}()
	}
	waitFor(t, "both pods", func() bool { return len(c.podNames()) == 2 })
	byStep := map[string]corev1.Pod{}
	for _, p := range c.pods() {
		byStep[p.Annotations[annoStep]] = p
	}
	for step, want := range map[string]string{"test": "8Gi", "security-go": "1Gi"} {
		p, ok := byStep[step]
		if !ok {
			t.Fatalf("no pod for step %s: %v", step, c.podNames())
		}
		var names []string
		for _, ct := range p.Spec.Containers {
			names = append(names, ct.Name)
			if ct.Name == "toolbox" {
				if got := ct.Resources.Limits.Memory().String(); got != want {
					t.Errorf("%s: memory limit %s, want its own %s", step, got, want)
				}
				if got := ct.Resources.Requests.Memory().String(); got != want {
					t.Errorf("%s: memory request %s, want its own %s", step, got, want)
				}
			}
		}
		if !slices.Equal(names, []string{helperName, "toolbox"}) {
			t.Errorf("%s: containers %v, want the helper and the step's own", step, names)
		}
	}
	if byStep["test"].Name == byStep["security-go"].Name {
		t.Fatal("two steps share a pod")
	}
	if err := os.WriteFile(hold, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	c.noPods()
	// Both steps' files are in the run's workspace afterwards: they ran at the same time on one workspace.
	dir := c.k.WorkspaceDir(a)
	if dir == "" || readFile(t, filepath.Join(dir, "from-a.txt")) != "a\n" || readFile(t, filepath.Join(dir, "from-b.txt")) != "b\n" {
		t.Fatalf("the workspace after both: %q", dir)
	}
}

// A run with many images never has a pod with more than the step's own
// container, the helper and the run's services: not the 14 containers an
// automatic Inspection made.
func TestAStepPodHoldsOnlyItsOwnContainerTheHelperAndTheServices(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"f": "x"})
	s := stepSpec(t, ws, "run_2", "inspect", "512Mi", "true", ContainerSpec{Name: "db", Image: "docker.io/library/postgres:17", Service: true})
	for i := 1; i <= 13; i++ {
		s.Containers = append(s.Containers, ContainerSpec{Name: fmt.Sprintf("image-%d", i), Image: fmt.Sprintf("registry.example/tool%d:1", i), Memory: "4Gi", MemoryLimit: "4Gi"})
	}
	if code, text, err := c.run(s); err != nil || code != 0 {
		t.Fatalf("code %d err %v\n%s", code, err, text)
	}
	if len(c.created) != 1 {
		t.Fatalf("%d pods were made", len(c.created))
	}
	var names []string
	var asked int64
	for _, ct := range c.created[0].Spec.Containers {
		names = append(names, ct.Name)
	}
	asked = podMemory(&c.created[0])
	if want := []string{helperName, "toolbox", "db"}; !slices.Equal(names, want) {
		t.Fatalf("containers %v, want %v", names, want)
	}
	if asked > memBytes("1Gi") {
		t.Fatalf("the pod asked for %d bytes: the other images' 4Gi leaked into it", asked)
	}
	c.noPods()
}

// The pod is gone after every way a step can end.
func TestTheStepPodIsGoneAfterEveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		cancel bool
		pull   string
		code   int
		err    string
	}{
		{name: "success", script: "true"},
		{name: "failure", script: "echo wrote > partial.txt; exit 3", code: 3},
		{name: "killed for memory", script: "exit 137", code: 137},
		{name: "canceled", script: "sleep 30", cancel: true, code: -1, err: "canceled"},
		{name: "image cannot be pulled", script: "true", pull: "ImagePullBackOff", code: -1, err: "cannot pull"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newStepCluster(t, KubernetesOptions{})
			ws := checkout(t, map[string]string{"f": "x"})
			s := stepSpec(t, ws, "run_3", "s", "256Mi", tc.script)
			if tc.pull != "" {
				c.waiting[s.Containers[0].Image] = tc.pull
			}
			ctx := context.Background()
			if tc.cancel {
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				go func() {
					waitFor(t, "the pod", func() bool { return len(c.podNames()) > 0 })
					time.Sleep(300 * time.Millisecond)
					stop()
				}()
			}
			var l lines
			code, err := c.k.Run(ctx, s, l.sink)
			if code != tc.code || (tc.err == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("code %d, err %v\n%s", code, err, l.text())
			}
			c.noPods()
			if tc.name == "failure" {
				// What a failed step left is kept: reports of a failed test are read from it.
				if got := readFile(t, filepath.Join(c.k.WorkspaceDir(s), "partial.txt")); got != "wrote\n" {
					t.Fatalf("partial.txt: %q", got)
				}
			}
			if tc.name == "killed for memory" {
				if note := c.k.ExplainExit(context.Background(), s, 137); !strings.Contains(note, "most likely because it used more memory than its limit (256Mi)") {
					t.Fatalf("the memory kill is explained after the pod is gone: %q", note)
				}
			}
		})
	}
}

// What one step leaves in the workspace, the next sees: written files, removed
// files, the execute bit, links, times; and the checkout itself is never
// written to.
func TestTheWorkspaceGoesFromStepToStep(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"README": "hello\n", "src/main.go": "package main\n", "old/dir/file": "x"})
	old := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Symlink("src/main.go", filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	s1 := stepSpec(t, ws, "run_4", "build", "256Mi", `
rm README; rm -rf old
echo "package main // changed" > src/main.go
mkdir -p dist; printf '#!/bin/sh\necho hi\n' > dist/tool; chmod +x dist/tool
echo cached > dist/cache.bin; touch -d @`+fmt.Sprint(old.Unix())+` dist/cache.bin
ln -s dist/tool tool-link`)
	if code, text, err := c.run(s1); err != nil || code != 0 {
		t.Fatalf("step 1: %d %v\n%s", code, err, text)
	}
	// A later step, of another image, sees all of it.
	s2 := stepSpec(t, ws, "run_4", "test", "128Mi", `
[ ! -e README ] || { echo README is back; exit 1; }
[ ! -e old ] || { echo old is back; exit 1; }
grep -q changed src/main.go || { echo main.go not changed; exit 1; }
[ -x dist/tool ] || { echo dist/tool lost its execute bit; exit 1; }
[ "$(readlink tool-link)" = dist/tool ] || { echo link lost; exit 1; }
[ "$(readlink link)" = src/main.go ] || { echo the checkout link lost; exit 1; }
echo second > second.txt`)
	s2.Containers[0].Image, s2.Containers[0].Name = "docker.io/library/busybox:1.37", "toolbox"
	if code, text, err := c.run(s2); err != nil || code != 0 {
		t.Fatalf("step 2: %d %v\n%s", code, err, text)
	}
	dir := c.k.WorkspaceDir(s1)
	if got := readFile(t, filepath.Join(dir, "second.txt")); got != "second\n" {
		t.Fatalf("second.txt %q", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "dist", "cache.bin"))
	if err != nil || fi.ModTime().Unix() != old.Unix() {
		t.Fatalf("a cached file keeps its time: %v %v (want %v)", fi, err, old)
	}
	// The checkout is as it was.
	if readFile(t, filepath.Join(ws, "README")) != "hello\n" || readFile(t, filepath.Join(ws, "src/main.go")) != "package main\n" ||
		!exists(filepath.Join(ws, "old/dir/file")) || exists(filepath.Join(ws, "second.txt")) {
		t.Fatal("the checkout was written to")
	}
	c.noPods()
}

// Steps that run at the same time each start from the workspace as it was and
// hand back only what they changed: neither puts back what the other removed
// or changed.
func TestStepsAtTheSameTimeMergeAndNeitherUndoesTheOther(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"README": "hello\n", "shared.txt": "base\n", "keep.txt": "keep\n"})
	gate := filepath.Join(t.TempDir(), "both-started")
	started := t.TempDir()
	body := func(id, work string) string {
		return fmt.Sprintf("touch %s/%s; while [ ! -e %s ]; do sleep 0.02; done; %s", started, id, gate, work)
	}
	a := stepSpec(t, ws, "run_5", "a", "256Mi", body("a", "rm README; echo from-a > a.txt; echo a-edit > shared.txt"))
	b := stepSpec(t, ws, "run_5", "b", "256Mi", body("b", "echo from-b > b.txt"))
	var wg sync.WaitGroup
	for _, s := range []Spec{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, text, err := c.run(s); err != nil || code != 0 {
				t.Errorf("%s: %d %v\n%s", s.StepID, code, err, text)
			}
		}()
	}
	waitFor(t, "both started", func() bool { return exists(filepath.Join(started, "a")) && exists(filepath.Join(started, "b")) })
	os.WriteFile(gate, nil, 0o644)
	wg.Wait()
	dir := c.k.WorkspaceDir(a)
	if readFile(t, filepath.Join(dir, "a.txt")) != "from-a\n" || readFile(t, filepath.Join(dir, "b.txt")) != "from-b\n" {
		t.Fatal("a file of one of the steps is missing")
	}
	if exists(filepath.Join(dir, "README")) {
		t.Error("what step a removed was put back by step b, which did not touch it")
	}
	if got := readFile(t, filepath.Join(dir, "shared.txt")); got != "a-edit\n" {
		t.Errorf("what step a changed was undone by step b, which did not touch it: %q", got)
	}
	if readFile(t, filepath.Join(dir, "keep.txt")) != "keep\n" {
		t.Error("an untouched file changed")
	}
	c.noPods()
}

// Reports, artifacts, caches and build contexts are read from, and put in,
// the run's workspace on the Station: before the first step's pod exists and
// after the last step's pod is gone.
func TestFilesAreReadFromAndPutInTheRunsWorkspaceWithoutAPod(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"f": "x"})
	s := stepSpec(t, ws, "run_6", "test", "256Mi", `cat restored.txt > seen.txt; mkdir -p reports; echo '<testsuite/>' > reports/junit.xml; mkdir -p dist; echo a > dist/a; echo b > dist/b`)
	if c.k.WorkspaceInPod(s) {
		t.Fatal("the workspace is set up before anything ran")
	}
	// A cache is restored, an artifact fetched, before the step.
	src := filepath.Join(t.TempDir(), "restored")
	os.WriteFile(src, []byte("from the cache\n"), 0o644)
	if err := c.k.PlaceFiles(context.Background(), s, []PlacedFile{{Path: "restored.txt", Local: src, Mode: 0o644}}, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if !c.k.WorkspaceInPod(s) || len(c.pods()) != 0 {
		t.Fatalf("placing files needs the Station's copy and no pod: %v", c.podNames())
	}
	if code, text, err := c.run(s); err != nil || code != 0 {
		t.Fatalf("%d %v\n%s", code, err, text)
	}
	c.noPods()
	if got := readFile(t, filepath.Join(c.k.WorkspaceDir(s), "seen.txt")); got != "from the cache\n" {
		t.Fatalf("the step did not see the file placed before it: %q", got)
	}
	got, err := c.k.CollectFiles(context.Background(), s, []string{"reports/*.xml"}, CollectLimits{}.WithDefaults())
	if err != nil || len(got.Files) != 1 || got.Files[0].Path != "reports/junit.xml" || string(got.Files[0].Data) != "<testsuite/>\n" {
		t.Fatalf("collected %+v, %v", got, err)
	}
	staged, err := c.k.StageFiles(context.Background(), s, []string{"dist"}, StageLimits{}.withDefaults(), t.TempDir())
	if err != nil || len(staged.Files) != 2 {
		t.Fatalf("staged %+v, %v", staged, err)
	}
	// Build context export is the Station's own directory.
	if ok, err := c.k.ExportContext(context.Background(), s, ContextSpec{Dir: "."}, t.TempDir(), ExportLimits{}); ok || err != nil {
		t.Fatalf("ExportContext: %v %v", ok, err)
	}
}

// The sampler reads the cgroup of the step's own container, in the step's own pod.
func TestTheUsageIsReadFromTheStepsOwnPodAndContainer(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"f": "x"})
	s := stepSpec(t, ws, "run_7", "measured", "2Gi", "true")
	s.Usage = &Usage{}
	if code, text, err := c.run(s); err != nil || code != 0 {
		t.Fatalf("%d %v\n%s", code, err, text)
	}
	pod := c.created[0].Name
	sampled := false
	for _, e := range c.execs {
		if e.pod == pod && e.container == "toolbox" {
			sampled = true
		}
		if e.pod != pod {
			t.Errorf("an exec went to %s, not the step's pod %s", e.pod, pod)
		}
	}
	if !sampled {
		t.Fatalf("nothing was exec'd in the step's container: %v", c.execs)
	}
	if s.Usage.ReservedMemory != memBytes("2Gi") {
		t.Errorf("reserved %d: the step's own container's 2Gi", s.Usage.ReservedMemory)
	}
}

func TestStepPodNamesAreDNSLabelsAndUniquePerAttempt(t *testing.T) {
	seen := map[string]Spec{}
	base := Spec{RunID: "run_ABC_123", StepID: "build", Attempt: 1, LeaseID: "lease_1", Name: "launchway-lease_1"}
	check := func(s Spec, owner string) {
		t.Helper()
		n := stepPodName(s, owner)
		if len(n) > 63 || !dnsLabel.MatchString(n) || len(validation.IsDNS1123Label(n)) > 0 {
			t.Errorf("%q is not a DNS label", n)
		}
		if prev, dup := seen[n]; dup {
			t.Errorf("%q is the name of %+v and of %+v", n, prev, s)
		}
		seen[n] = s
	}
	check(base, "o1")
	for _, mod := range []func(*Spec){
		func(s *Spec) { s.RunID = "run_other" },
		func(s *Spec) { s.StepID = "test" },
		func(s *Spec) { s.Attempt = 2 },
		func(s *Spec) { s.LeaseID = "lease_2"; s.Name = "launchway-lease_2" },
		func(s *Spec) { s.StepID = "group/Build Step_1 (linux/amd64)" },
		func(s *Spec) { s.StepID = strings.Repeat("very-long-step-name-", 10) },
		func(s *Spec) { s.StepID = "../../ÄÖ" },
		func(s *Spec) { s.StepID = "" },
	} {
		s := base
		mod(&s)
		check(s, "o1")
	}
	check(base, "o2") // another Station
	again := base
	if first, second := stepPodName(base, "o1"), stepPodName(again, "o1"); first != second {
		t.Error("the name is not stable")
	}
}

func TestThePodSettingIsChecked(t *testing.T) {
	for _, tc := range []struct {
		pod, want string
		bad       bool
	}{{"", PodPerStep, false}, {"per-step", PodPerStep, false}, {"per-run", PodPerRun, false}, {"per-stage", "", true}} {
		o := KubernetesOptions{Pod: tc.pod}
		err := o.setDefaults()
		if tc.bad != (err != nil) || (!tc.bad && o.Pod != tc.want) {
			t.Errorf("%q: %q %v", tc.pod, o.Pod, err)
		}
	}
}

// Acceptance case 3, which the per-run pod could not escape. A retried step
// waited for ever for quota that its own run's abandoned pod held. Now the
// retry releases the pods its own run holds that no step of this process runs
// in, says so, and goes ahead.
func TestARetriedStepNeverWaitsForRoomItsOwnAbandonedPodHolds(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	c.quota("16Gi")
	ws := checkout(t, map[string]string{"f": "x"})
	// The first attempt's Station process stopped (or its lease was lost and nothing deleted its pod); the
	// pod is left holding 9Gi.
	abandoned := runPodObj(c.k, "run_8", "me", time.Hour)
	abandoned.Name = "launchway-test-a1-abandoned"
	abandoned.Labels[labelKind] = "step"
	abandoned.Annotations = map[string]string{annoStep: "test", annoAttempt: "1"}
	abandoned.Spec.Containers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("9Gi")}
	if _, err := c.cs.CoreV1().Pods(stepNS).Create(context.Background(), abandoned, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	pod := *abandoned

	// The retry runs in a new process of the same Station.
	k2 := c.sibling()
	if c.hard < memBytes("16Gi") {
		t.Fatal("the quota is not what the test says")
	}
	retry := stepSpec(t, ws, "run_8", "test", "9Gi", "echo ran > retried.txt")
	retry.Attempt, retry.LeaseID, retry.Name = 2, "lease-second", "launchway-lease-second"
	var l lines
	code, err := k2.Run(context.Background(), retry, l.sink)
	if err != nil || code != 0 {
		t.Fatalf("the retry waited for its own run's pod: code %d err %v\n%s", code, err, l.text())
	}
	if !strings.Contains(l.text(), "Released pod "+pod.Name) || !strings.Contains(l.text(), "no longer holds a lease") {
		t.Fatalf("the log does not say what was released:\n%s", l.text())
	}
	c.noPods()
}

// A step whose quota is held by other pods waits, and the message says what is
// true: what the namespace allows, what is reserved, who holds it, and that
// nothing ran out of memory. It leaves no pod behind.
func TestAStepWaitsForRoomHeldByOthersAndSaysWhoHoldsIt(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	c.quota("16Gi")
	ws := checkout(t, map[string]string{"f": "x"})
	// Another run holds 8Gi; this run's own first step, still running here, holds 6Gi.
	other := runPodObj(c.k, "run_other", "them", time.Minute)
	other.Spec.Containers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("8Gi")}
	other.Annotations = map[string]string{annoStep: "elsewhere"}
	other.Labels[labelKind] = "step"
	if _, err := c.cs.CoreV1().Pods(stepNS).Create(context.Background(), other, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	hold := filepath.Join(t.TempDir(), "release")
	running := stepSpec(t, ws, "run_9", "first", "6Gi", fmt.Sprintf("while [ ! -e %s ]; do sleep 0.02; done", hold))
	done := make(chan struct{})
	go func() { defer close(done); c.run(running) }()
	waitFor(t, "the first step", func() bool { return len(c.podNames()) == 2 })

	var l lines
	code, err := c.k.Run(context.Background(), stepSpec(t, ws, "run_9", "second", "4Gi", "true"), l.sink)
	capErr, ok := AsCapacity(err)
	if !ok || code != -1 || capErr.Code != WaitQuotaFull {
		t.Fatalf("want a wait for room, got code %d err %v", code, err)
	}
	for _, want := range []string{"this Sortie's steps first hold 6208Mi memory", "release it when they end", "the namespace allows 16Gi memory", "14400Mi memory is reserved in all", "needs 4160Mi memory more", "nothing ran out of memory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if n := len(c.podNames()); n != 2 {
		t.Fatalf("the waiting step left a pod: %v", c.podNames())
	}
	os.WriteFile(hold, nil, 0o644)
	<-done
	// The run's own running step is never released by a retry, only pods nothing runs in.
	if got := c.podNames(); len(got) != 1 || !strings.Contains(got[0], "run-other") && got[0] != other.Name {
		t.Fatalf("left: %v", got)
	}
}

// A step whose own pod can never fit the namespace's quota fails at once, with
// numbers, instead of waiting.
func TestAStepThatCanNeverFitTheNamespaceFailsAtOnce(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	c.cs.CoreV1().ResourceQuotas(stepNS).Create(context.Background(), &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "steps", Namespace: stepNS},
		Status: corev1.ResourceQuotaStatus{Hard: corev1.ResourceList{corev1.ResourceRequestsMemory: resource.MustParse("16Gi")},
			Used: corev1.ResourceList{corev1.ResourceRequestsMemory: resource.MustParse("15Gi")}}}, metav1.CreateOptions{})
	ws := checkout(t, map[string]string{"f": "x"})
	var l lines
	code, err := c.k.Run(context.Background(), stepSpec(t, ws, "run_10", "huge", "20Gi", "true"), l.sink)
	if err == nil || code != -1 {
		t.Fatalf("code %d err %v", code, err)
	}
	if _, wait := AsCapacity(err); wait || errors.Is(err, ErrInfrastructure) {
		t.Fatalf("a step that can never fit must not wait or be retried: %v", err)
	}
	for _, want := range []string{"the step's pod asks for requests.memory", "above the 16Gi", `ResourceQuota "steps"`, "can never start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	c.noPods()
	// And a step that fits, while the quota's own figures say 15Gi are used, is not made to wait by those
	// figures: the API server's answer, from its live count, decides.
	code, err = c.k.Run(context.Background(), stepSpec(t, ws, "run_10", "small", "4Gi", "true"), l.sink)
	if err != nil || code != 0 {
		t.Fatalf("a step that fits was held back by stale figures: %d %v", code, err)
	}
}

// Acceptance case 5 and the sweeper. A Station that restarts does not adopt a
// step that was running (its lease is gone and the step runs again on a
// lease of its own); it removes the pods of attempts that no longer hold a
// lease, so that no quota is held by them, and leaves other Stations' pods of
// runs that are still going.
func TestARestartedStationSweepsThePodsOfAttemptsThatHoldNoLease(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ctx := context.Background()
	mk := func(run, owner, step string, age time.Duration) *corev1.Pod {
		p := runPodObj(c.k, run, owner, age)
		p.Name = fmt.Sprintf("launchway-%s-%s-%s", step, run, owner)
		p.Labels[labelKind] = "step"
		p.Annotations = map[string]string{annoStep: step, annoAttempt: "1"}
		if _, err := c.cs.CoreV1().Pods(stepNS).Create(ctx, p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	idle := mk("run-a", "me", "idle", time.Minute)         // mine, no step of this process
	running := mk("run-a", "me", "running", time.Minute)   // mine, a step of this process runs in it
	theirs := mk("run-a", "them", "theirs", time.Hour)     // another Station's, its run still going
	theirsDone := mk("run-b", "them", "done", time.Minute) // another Station's, its run ended
	ancient := mk("run-c", "them", "ancient", 48*time.Hour)
	c.k.setActive(running.Name, "run-a")
	c.k.maxPodAge = 24 * time.Hour
	say := serverSays(RunInfo{ID: "run-a", Status: "running"}, RunInfo{ID: "run-b", Status: "failed", FinishedAt: time.Now().Add(-time.Minute).UnixMilli()},
		RunInfo{ID: "run-c", Status: "running"})
	if err := c.k.Cleanup(ctx, say); err != nil {
		t.Fatal(err)
	}
	have := func(p *corev1.Pod) bool {
		_, err := c.cs.CoreV1().Pods(stepNS).Get(ctx, p.Name, metav1.GetOptions{})
		return err == nil
	}
	for p, want := range map[*corev1.Pod]bool{idle: false, running: true, theirs: true, theirsDone: false, ancient: false} {
		if have(p) != want {
			t.Errorf("%s: present %v, want %v", p.Name, have(p), want)
		}
	}
}

// A lost lease: the pod of the step that was taken away is released, and one
// that still executes is not.
func TestReleaseRunDeletesTheIdlePodsOfTheRunAndNotARunningStep(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ctx := context.Background()
	hold := filepath.Join(t.TempDir(), "go")
	ws := checkout(t, map[string]string{"f": "x"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.run(stepSpec(t, ws, "run_11", "running", "64Mi", fmt.Sprintf("while [ ! -e %s ]; do sleep 0.02; done", hold)))
	}()
	waitFor(t, "the running step", func() bool { return len(c.podNames()) == 1 })
	stray := runPodObj(c.k, "run_11", "me", time.Minute)
	stray.Name, stray.Labels[labelKind] = "launchway-stray", "step"
	c.cs.CoreV1().Pods(stepNS).Create(ctx, stray, metav1.CreateOptions{})
	other := runPodObj(c.k, "run_11", "them", time.Minute)
	other.Name, other.Labels[labelKind] = "launchway-theirs", "step"
	c.cs.CoreV1().Pods(stepNS).Create(ctx, other, metav1.CreateOptions{})

	if err := c.k.ReleaseRun(ctx, "run_11"); err != nil {
		t.Fatal(err)
	}
	names := c.podNames()
	if len(names) != 2 || slices.Contains(names, "launchway-stray") || !slices.Contains(names, "launchway-theirs") {
		t.Fatalf("after the release: %v", names)
	}
	os.WriteFile(hold, nil, 0o644)
	<-done
	if got := c.podNames(); len(got) != 1 || got[0] != "launchway-theirs" {
		t.Fatalf("left: %v", got)
	}
}

// The copies of the workspaces of runs that ended are removed, and a copy in
// use is not.
func TestTheWorkspaceCopyOfARunThatEndedIsRemoved(t *testing.T) {
	c := newStepCluster(t, KubernetesOptions{})
	ws := checkout(t, map[string]string{"f": "x"})
	for _, run := range []string{"run_done", "run_going"} {
		if code, text, err := c.run(stepSpec(t, ws, run, "s", "64Mi", "true")); err != nil || code != 0 {
			t.Fatalf("%d %v %s", code, err, text)
		}
	}
	say := serverSays(RunInfo{ID: "run_done", Status: "succeeded", FinishedAt: time.Now().UnixMilli()}, RunInfo{ID: "run_going", Status: "running"})
	if err := c.k.Cleanup(context.Background(), say); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(c.k.workRoot(), "run_done")) || exists(filepath.Join(c.k.workRoot(), "run_done.id")) {
		t.Error("the copy of a finished run is still there")
	}
	if !exists(filepath.Join(c.k.workRoot(), "run_going")) {
		t.Error("the copy of a run that is going was removed")
	}
	// The run's workspace is made again from the checkout, if the run is somehow still asked for.
	if got := c.k.WorkspaceDir(stepSpec(t, ws, "run_done", "s", "64Mi", "true")); got != "" {
		t.Errorf("the finished run still has a copy: %s", got)
	}
}

// What the extractor refuses: names that leave the workspace.
func TestHandingBackNeverWritesOutsideTheWorkspace(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	os.MkdirAll(ws, 0o755)
	for _, name := range []string{"../escape", "/abs", "a/../../escape"} {
		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(tarOf(map[string]string{name: "x"})(pw)) }()
		if err := extractWorkspace(pr, ws, handOverLimits, nil, nil); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
	if exists(filepath.Join(dir, "escape")) || exists("/abs") {
		t.Fatal("a file was written outside")
	}
	// A symbolic link that points out is kept as a link, never written through.
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(tarSymlinkThenFile(pw)) }()
	if err := extractWorkspace(pr, ws, handOverLimits, nil, nil); err == nil && exists(filepath.Join(dir, "outside-target", "file")) {
		t.Fatal("a file was written through a link")
	}
}

// tarOf writes a tar stream of regular files with exactly the names given.
func tarOf(files map[string]string) func(io.Writer) error {
	return func(w io.Writer) error {
		tw := tar.NewWriter(w)
		for name, content := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			if _, err := tw.Write([]byte(content)); err != nil {
				return err
			}
		}
		return tw.Close()
	}
}

// tarSymlinkThenFile is a link that points out of the workspace, then a file
// below it.
func tarSymlinkThenFile(w io.Writer) error {
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Name: "out", Typeflag: tar.TypeSymlink, Linkname: "../outside-target", Mode: 0o777}); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "out/file", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	tw.Write([]byte("x"))
	return tw.Close()
}

func memBytes(q string) int64 { r := resource.MustParse(q); return r.Value() }

// mebi writes bytes as the API server does, in Mi.
func mebi(n int64) string { return fmt.Sprintf("%dMi", n>>20) }
