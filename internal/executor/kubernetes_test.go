package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These tests need a real cluster whose test namespace enforces Pod Security
// "restricted", for example:
//
//	kind create cluster --name launchway-test
//	kubectl --context kind-launchway-test create namespace launchway-test
//	kubectl --context kind-launchway-test label --overwrite namespace launchway-test pod-security.kubernetes.io/enforce=restricted
//	LAUNCHWAY_TEST_KUBE_CONTEXT=kind-launchway-test go test ./internal/executor/ -run Kubernetes
func kube(t *testing.T) *Kubernetes {
	t.Helper()
	ctxName := os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT")
	if ctxName == "" {
		t.Skip("set LAUNCHWAY_TEST_KUBE_CONTEXT to run Kubernetes tests")
	}
	k, err := NewKubernetes(KubernetesOptions{Context: ctxName, Namespace: testKubeNamespace(), StartTimeout: "5m"}, "test-"+t.Name())
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

// On Kubernetes the service runs as the pod's user like the steps, so it is
// one that can (nginx's official image cannot): BusyBox's httpd on a high
// port, serving a page that says which user it runs as.
var kubeWebService = ContainerSpec{Name: "web", Image: "docker.io/library/busybox:1.37", Service: true,
	Command: []string{"sh", "-c", "mkdir -p /tmp/www && echo served-by-uid-$(id -u) > /tmp/www/index.html && exec httpd -f -p 8080 -h /tmp/www"}}

const reachKubeService = `for i in $(seq 1 30); do wget -q -O - http://localhost:8080/ 2>/dev/null | grep served-by && exit 0; sleep 1; done; exit 1`

func TestKubernetesService(t *testing.T) {
	k := kube(t)
	runID := fmt.Sprintf("run_ks%d", time.Now().UnixNano())
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(context.Background(), podName(runID, k.owner), metav1.DeleteOptions{})
	s := kspec(t, runID, "s", alpine, reachKubeService, t.TempDir())
	s.Containers = append(append([]ContainerSpec{}, twoTools...), kubeWebService)
	var c capture
	code, err := k.Run(context.Background(), s, c.sink)
	if err != nil || code != 0 || !strings.Contains(c.text(), "stdout: served-by-uid-1000") {
		t.Fatalf("code %d err %v\n%s", code, err, c.text())
	}
	if !strings.Contains(c.text(), "web (service)") {
		t.Fatalf("the start message should list the service:\n%s", c.text())
	}
}

// requireRestrictedNamespace makes sure the tests below really ran under
// Pod Security "restricted" (make test-kubernetes labels the namespace).
func requireRestrictedNamespace(t *testing.T, k *Kubernetes) {
	t.Helper()
	ns, err := k.client.CoreV1().Namespaces().Get(context.Background(), k.o.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ns.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Fatalf("namespace %s must enforce Pod Security restricted: kubectl --context <test context> label --overwrite ns %s pod-security.kubernetes.io/enforce=restricted",
			k.o.Namespace, k.o.Namespace)
	}
}

// Steps run in real build images, admitted by a namespace that enforces Pod
// Security "restricted", as a user the images do not know: the checkout is
// theirs to change and Git accepts it, and HOME, /tmp and the package
// managers' caches are writable.
func TestKubernetesRestrictedRealImages(t *testing.T) {
	k := kube(t)
	requireRestrictedNamespace(t, k)
	ctx := context.Background()
	runID := fmt.Sprintf("run_kr%d", time.Now().UnixNano())
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(runID, k.owner), metav1.DeleteOptions{})

	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "from-checkout.txt"), []byte("checked out\n"), 0o644)
	gitCmd := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", ws, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitCmd("init", "-q", "-b", "main")
	gitCmd("add", ".")
	gitCmd("commit", "-q", "-m", "first")

	containers := []ContainerSpec{
		{Name: "python", Image: "docker.io/library/python:3.12-slim"},
		{Name: "node", Image: "docker.io/library/node:24-alpine"},
		{Name: "git", Image: "docker.io/alpine/git:v2.52.0"},
	}
	step := func(name, container, script string, env map[string]string) string {
		t.Helper()
		var c capture
		s := Spec{RunID: runID, Containers: containers, Container: container, Name: name, Script: script, Env: env,
			Workspace: ws, MetaDir: t.TempDir()}
		code, err := k.Run(ctx, s, c.sink)
		if err != nil || code != 0 {
			t.Fatalf("step %s: code %d err %v\n%s", name, code, err, c.text())
		}
		t.Logf("step %s:\n%s", name, c.text())
		return c.text()
	}

	out := step("files", "python", `test "$(id -u)" = 1000 && test "$(id -g)" = 1000
echo "HOME=$HOME; /workspace is owned by $(stat -c %u:%g /workspace)"
echo edited >> from-checkout.txt
mkdir -p build && echo made > build/out.txt
echo in-home > "$HOME/note" && cat "$HOME/note"
echo in-tmp > /tmp/note && cat /tmp/note
python3 -c 'import tempfile; tempfile.NamedTemporaryFile(); print("python tempfile ok")'`, nil)
	for _, want := range []string{"stdout: HOME=" + stepHome + ";", "stdout: in-home", "stdout: in-tmp", "stdout: python tempfile ok",
		"system: Containers run as user 1000 (group 1000), not as root"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}

	// Git, in another image, accepts the checkout and sees the change.
	out = step("git", "git", `git status --porcelain; git rev-parse --abbrev-ref HEAD`, nil)
	if !strings.Contains(out, "stdout:  M from-checkout.txt") || !strings.Contains(out, "stdout: main") || strings.Contains(out, "dubious") {
		t.Fatalf("git:\n%s", out)
	}

	// A step's own HOME wins over the pod's.
	out = step("own-home", "node", `echo "HOME=$HOME"`, map[string]string{"HOME": "/workspace/.myhome"})
	if !strings.Contains(out, "stdout: HOME=/workspace/.myhome") {
		t.Fatalf("own HOME:\n%s", out)
	}

	// The package managers, if the cluster can reach their registries.
	out = step("pip", "python", `pip install --user --disable-pip-version-check -q six && python3 -c 'import six; print("six", six.__version__)'
ls "$PIP_CACHE_DIR" >/dev/null && echo pip cache in $PIP_CACHE_DIR`, nil)
	if !strings.Contains(out, "stdout: pip cache in "+stepHome+"/.cache/pip") {
		t.Fatalf("pip:\n%s", out)
	}
	out = step("npm", "node", `npm install --no-audit --no-fund --loglevel=error is-number@7 && node -e 'console.log("is-number", require("is-number")(5))'
echo "npm cache: $(npm config get cache)"`, nil)
	if !strings.Contains(out, "stdout: is-number true") || !strings.Contains(out, "stdout: npm cache: "+stepHome+"/.npm") {
		t.Fatalf("npm:\n%s", out)
	}
}

// Without the restricted security context, a namespace that enforces Pod
// Security "restricted" refuses the pod, and the step's error says why.
func TestKubernetesUnrestrictedRefusedByRestrictedNamespace(t *testing.T) {
	k := kube(t)
	requireRestrictedNamespace(t, k)
	u, err := NewKubernetes(KubernetesOptions{Context: os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT"), Namespace: testKubeNamespace(),
		Restricted: ptr(false)}, "test-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	runID := fmt.Sprintf("run_ku%d", time.Now().UnixNano())
	defer u.client.CoreV1().Pods(u.o.Namespace).Delete(context.Background(), podName(runID, u.owner), metav1.DeleteOptions{})
	_, err = u.Run(context.Background(), kspec(t, runID, "s", alpine, "true", t.TempDir()), func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), `violates PodSecurity "restricted`) {
		t.Fatalf("err %v", err)
	}
}

// A runner removes another runner's pods once their runs no longer need
// them, but never a pod of an active run, nor one of a run the server does
// not know (it may belong to someone else).
func TestKubernetesCleanupAcrossRunners(t *testing.T) {
	a := kube(t)
	b, err := NewKubernetes(KubernetesOptions{Context: os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT"), Namespace: testKubeNamespace()}, "test-other-"+t.Name())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pods := a.client.CoreV1().Pods(a.o.Namespace)
	stamp := time.Now().UnixNano()
	finished, active, unknown := fmt.Sprintf("run_kf%d", stamp), fmt.Sprintf("run_ka%d", stamp), fmt.Sprintf("run_ku%d", stamp)
	for _, id := range []string{finished, active, unknown} {
		defer pods.Delete(ctx, podName(id, a.owner), metav1.DeleteOptions{})
		s := kspec(t, id, "s", alpine, "true", t.TempDir())
		s.Containers, s.Container = twoTools[:1], "alpine"
		if code, err := a.Run(ctx, s, func(string, string) {}); err != nil || code != 0 {
			t.Fatalf("run %s: code %d err %v", id, code, err)
		}
	}
	lookup := func(context.Context, []string) (map[string]RunInfo, error) {
		now := time.Now().UnixMilli()
		return map[string]RunInfo{
			finished: {ID: finished, Status: "succeeded", FinishedAt: now},
			active:   {ID: active, Status: "running"},
		}, nil
	}
	if err := b.Cleanup(ctx, lookup); err != nil {
		t.Fatal(err)
	}
	gone := func(id string) bool {
		p, err := pods.Get(ctx, podName(id, a.owner), metav1.GetOptions{})
		return err != nil || p.DeletionTimestamp != nil
	}
	if !gone(finished) {
		t.Fatal("runner B left runner A's pod of a finished run")
	}
	if gone(active) {
		t.Fatal("runner B removed runner A's pod of an active run")
	}
	if gone(unknown) {
		t.Fatal("runner B removed a pod of a run the server does not know")
	}
	if err := a.Cleanup(ctx, lookup); err != nil {
		t.Fatal(err)
	}
	if !gone(unknown) || gone(active) {
		t.Fatalf("runner A's own clean-up: unknown gone %v, active gone %v", gone(unknown), gone(active))
	}
}

// Artifacts on Kubernetes: kept from one run's pod, and put into another
// run's pod, as when a later step runs on another runner.
func TestKubernetesArtifacts(t *testing.T) {
	k := kube(t)
	ctx := context.Background()
	run1 := fmt.Sprintf("run_ka%d", time.Now().UnixNano())
	run2 := run1 + "b"
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(run1, k.owner), metav1.DeleteOptions{})
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(run2, k.owner), metav1.DeleteOptions{})
	s1 := kspec(t, run1, "keep", alpine, artifactScript, t.TempDir())
	s2 := kspec(t, run2, "fetch", alpine, readFetched, t.TempDir())
	s2.WorkspaceID = "ws2"
	artifactsAcrossSteps(t, k, s1, s2)
}

// testKubeNamespace is the namespace Kubernetes tests run step pods in:
// LAUNCHWAY_TEST_KUBE_NAMESPACE, or launchway-test.
func testKubeNamespace() string {
	if ns := os.Getenv("LAUNCHWAY_TEST_KUBE_NAMESPACE"); ns != "" {
		return ns
	}
	return "launchway-test"
}
