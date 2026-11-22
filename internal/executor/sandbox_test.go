package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jk-magic-creations/launchway/internal/testlib"
)

func podmanFor(t *testing.T) *Container {
	t.Helper()
	bin, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("podman is not installed")
	}
	if out, err := exec.Command(bin, "image", "exists", "docker.io/library/alpine:3.22").CombinedOutput(); err != nil {
		t.Skipf("the image alpine:3.22 is not on this machine: %s", out)
	}
	return &Container{Bin: bin}
}

func runSandboxed(t *testing.T, c *Container, ws string, sb *Sandbox, script string) (string, error) {
	t.Helper()
	meta := t.TempDir()
	var mu sync.Mutex
	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), testlib.Slow(60*time.Second))
	defer cancel()
	_, err := c.Run(ctx, Spec{RunID: "r", StepID: "s", LeaseID: "l" + filepath.Base(ws), Name: "sandbox-" + filepath.Base(ws), Image: "docker.io/library/alpine:3.22",
		Script: script, Workspace: ws, MetaDir: meta, Sandbox: sb,
		Containers: []ContainerSpec{{Name: "a", Image: "docker.io/library/alpine:3.22"}}, Container: "a"},
		func(stream, text string) {
			mu.Lock()
			out.WriteString(text + "\n")
			mu.Unlock()
		})
	return out.String(), err
}

// A custom step's sandbox on Podman: no network, a read-only root filesystem
// and workspace, except the paths the manifest lists and the contract directory.
func TestPodmanSandboxEnforcesWhatItClaims(t *testing.T) {
	c := podmanFor(t)
	ws := t.TempDir()
	for _, d := range []string{"dist", "src"} {
		if err := os.MkdirAll(filepath.Join(ws, d), 0o777); err != nil {
			t.Fatal(err)
		}
	}
	got, err := runSandboxed(t, c, ws, &Sandbox{Name: "x", Write: []string{"dist"}, IODir: ".launchway/step-io/x"}, `
wget -q -T 3 -O- http://1.1.1.1/ >/dev/null 2>&1 && echo NETWORK || echo NO-NETWORK
( echo x > /workspace/src/nope ) 2>/dev/null && echo WROTE-SRC || echo SRC-READ-ONLY
( echo y > /workspace/dist/ok ) && echo WROTE-DIST
( echo z > /workspace/.launchway/step-io/x/outputs.json ) && echo WROTE-IO
( touch /usr/nope ) 2>/dev/null && echo WROTE-ROOT || echo ROOT-READ-ONLY
( echo t > /tmp/ok ) && echo WROTE-TMP
id -u >/dev/null
`)
	if err != nil {
		t.Fatalf("%v\n%s", err, got)
	}
	for _, want := range []string{"NO-NETWORK", "SRC-READ-ONLY", "WROTE-DIST", "WROTE-IO", "ROOT-READ-ONLY", "WROTE-TMP"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"WROTE-SRC", "WROTE-ROOT"} {
		if strings.Contains(got, bad) {
			t.Errorf("found %q in:\n%s", bad, got)
		}
	}
	if strings.Contains(strings.ReplaceAll(got, "NO-NETWORK", ""), "NETWORK") {
		t.Errorf("the step reached the network:\n%s", got)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "dist", "ok")); err != nil || strings.TrimSpace(string(b)) != "y" {
		t.Errorf("the listed path was not written: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "src", "nope")); err == nil {
		t.Error("a path that is not listed was written")
	}
}

// A writable path that is a link leading out of the workspace is refused: the
// step would otherwise be given the place the link leads to.
func TestPodmanSandboxRefusesAWritablePathThatIsALink(t *testing.T) {
	c := podmanFor(t)
	ws, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "dist")); err != nil {
		t.Fatal(err)
	}
	_, err := runSandboxed(t, c, ws, &Sandbox{Name: "x", Write: []string{"dist"}, IODir: ".launchway/step-io/x"}, `echo hi > /workspace/dist/pwned`)
	if err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("want a refusal naming the link, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); err == nil {
		t.Fatal("the step wrote through the link")
	}
}

func TestHostExecutorRefusesASandboxedStepUnlessAllowed(t *testing.T) {
	h := &Host{}
	_, err := h.Run(context.Background(), Spec{Script: "true", Workspace: t.TempDir(), MetaDir: t.TempDir(), Sandbox: &Sandbox{Name: "x"}}, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "sandbox") || !isCannotStart(err) {
		t.Fatalf("got %v", err)
	}
	h.AllowUnsandboxed = true
	if _, err := h.Run(context.Background(), Spec{Script: "true", Workspace: t.TempDir(), MetaDir: t.TempDir(), Sandbox: &Sandbox{Name: "x"}}, func(string, string) {}); err != nil {
		t.Fatalf("allowed: %v", err)
	}
}

func isCannotStart(err error) bool { return errors.Is(err, ErrCannotStart) }

// The pod of a sandboxed step on Kubernetes (no cluster needed): the label the
// chart's policies select on, a read-only root filesystem, no service account token.
func TestKubernetesPodOfASandboxedStep(t *testing.T) {
	k := testKube(t, KubernetesOptions{Pod: PodPerStep})
	pod, _ := k.podSpec("p", "run_1", []ContainerSpec{{Name: "a", Image: "docker.io/library/alpine:3.22"}})
	k.sandboxPod(pod, &Sandbox{Name: "x", IODir: ".launchway/step-io/x"}, func(string, ...any) {})
	if pod.Labels[labelSandbox] != "custom" || pod.Labels[labelNet] != "none" {
		t.Errorf("labels %v", pod.Labels)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Error("the pod has a service account token")
	}
	for _, c := range pod.Spec.Containers {
		if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
			t.Errorf("container %s has a writable root filesystem", c.Name)
		}
	}
	pod, _ = k.podSpec("p", "run_1", []ContainerSpec{{Name: "a", Image: "docker.io/library/alpine:3.22"}})
	k.sandboxPod(pod, &Sandbox{Name: "x", Network: []string{"registry.npmjs.org"}}, func(string, ...any) {})
	if pod.Labels[labelNet] != "hosts" || pod.Annotations[annoHosts] != "registry.npmjs.org" {
		t.Errorf("labels %v annotations %v", pod.Labels, pod.Annotations)
	}
}

// On a cluster (LAUNCHWAY_TEST_KUBE_CONTEXT, which must be the kind test
// cluster): a sandboxed step's pod is admitted under Pod Security
// "restricted", has a read-only root filesystem, and, with the network policy
// the chart installs for the label, reaches nothing. The namespace is made
// for the test and removed after it.
func TestKubernetesSandboxedStepOnACluster(t *testing.T) {
	ctxName := os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT")
	if ctxName == "" {
		t.Skip("set LAUNCHWAY_TEST_KUBE_CONTEXT to run Kubernetes tests")
	}
	if ctxName != "kind-launchway-test" {
		t.Skipf("this test runs only against the kind test cluster, not %s", ctxName)
	}
	ns := fmt.Sprintf("launchway-sandbox-%d", time.Now().UnixNano()%1_000_000_000)
	kubectl := func(stdin string, args ...string) {
		t.Helper()
		cmd := exec.Command("kubectl", append([]string{"--context", ctxName}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("kubectl %v: %v\n%s", args, err, out)
		}
	}
	kubectl("", "create", "namespace", ns)
	t.Cleanup(func() {
		_ = exec.Command("kubectl", "--context", ctxName, "delete", "namespace", ns, "--wait=false").Run()
	})
	kubectl("", "label", "--overwrite", "namespace", ns, "pod-security.kubernetes.io/enforce=restricted")
	// What the chart installs for custom steps' pods: no egress unless the step lists hosts.
	kubectl(fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: sandbox, namespace: %s }
spec:
  podSelector: { matchLabels: { launchway.dev/step-sandbox: custom } }
  policyTypes: [Ingress, Egress]
  ingress: []
  egress: []
`, ns), "apply", "-f", "-")
	k, err := NewKubernetes(KubernetesOptions{Context: ctxName, Namespace: ns, StartTimeout: "4m", Pod: PodPerStep}, "test-sandbox")
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	var c capture
	code, err := k.Run(context.Background(), Spec{RunID: fmt.Sprintf("run_sb%d", time.Now().UnixNano()), StepID: "s", Attempt: 1, LeaseID: "l1",
		Containers: []ContainerSpec{{Name: "alpine", Image: alpine}}, Container: "alpine", Name: "sb", Image: alpine,
		Script: `( touch /usr/nope ) 2>/dev/null && echo WROTE-ROOT || echo ROOT-READ-ONLY
( echo ok > /workspace/f ) && echo WROTE-WORKSPACE
wget -q -T 4 -O- http://1.1.1.1/ >/dev/null 2>&1 && echo NETWORK || echo NO-NETWORK
`,
		Workspace: ws, MetaDir: t.TempDir(), Sandbox: &Sandbox{Name: "x", IODir: ".launchway/step-io/x"}}, c.sink)
	if err != nil || code != 0 {
		t.Fatalf("code %d err %v\n%s", code, err, c.text())
	}
	for _, want := range []string{"ROOT-READ-ONLY", "WROTE-WORKSPACE", "NO-NETWORK"} {
		if !strings.Contains(c.text(), want) {
			t.Errorf("missing %s in:\n%s", want, c.text())
		}
	}
}
