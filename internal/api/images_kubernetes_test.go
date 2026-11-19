package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/crypto/bcrypt"

	"github.com/jk-magic-creations/launchway/internal/engine"
	"github.com/jk-magic-creations/launchway/internal/executor"
	"github.com/jk-magic-creations/launchway/internal/imagebuild"
	"github.com/jk-magic-creations/launchway/internal/imagebuild/imagetest"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
	"github.com/jk-magic-creations/launchway/internal/secrets"
)

// buildNamespace is where the Kubernetes test runs BuildKit and a registry,
// apart from the step namespace (launchway-test), which enforces Pod
// Security "restricted".
const buildNamespace = "launchway-build-test"

type kubectl struct {
	t       *testing.T
	context string
}

func (k kubectl) run(stdin string, args ...string) string {
	k.t.Helper()
	cmd := exec.Command("kubectl", append([]string{"--context", k.context}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		k.t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// portForward forwards a local port to a service, until the test ends.
func (k kubectl) portForward(ns, target string, port int) int {
	k.t.Helper()
	cmd := exec.Command("kubectl", "--context", k.context, "-n", ns, "port-forward", "--address", "127.0.0.1", target, fmt.Sprintf(":%d", port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		k.t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		k.t.Fatal(err)
	}
	k.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	re := regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			go func() {
				for sc.Scan() {
				}
			}()
			var p int
			fmt.Sscan(m[1], &p)
			return p
		}
	}
	k.t.Fatalf("kubectl port-forward %s gave no port", target)
	return 0
}

// The image step on Kubernetes: the step pods run in a namespace that
// enforces Pod Security "restricted"; BuildKit runs rootless, deployed from
// deploy/buildkit/buildkitd.yaml into a namespace of its own; a registry
// with a password runs in the cluster too. A step writes files in the run's
// pod; the image step copies the context out of the pod, builds it on that
// BuildKit and pushes it; the image pulled back has the pod's files.
func TestKubernetesImageStep(t *testing.T) {
	ctxName := os.Getenv("LAUNCHWAY_TEST_KUBE_CONTEXT")
	if ctxName == "" {
		t.Skip("set LAUNCHWAY_TEST_KUBE_CONTEXT to run Kubernetes tests")
	}
	if !strings.HasPrefix(ctxName, "kind-") {
		t.Fatalf("this test installs things into the cluster and changes a node; it runs only on a kind cluster, not %s", ctxName)
	}
	k := kubectl{t, ctxName}
	root, _ := filepath.Abs("../..")

	// The node gets the seccomp profile, as an operator would install it.
	node := strings.TrimPrefix(ctxName, "kind-") + "-control-plane"
	engineBin := imagetest.Engine(t)
	if out, err := exec.Command(engineBin, "exec", node, "mkdir", "-p", "/var/lib/kubelet/seccomp/profiles").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := exec.Command(engineBin, "cp", filepath.Join(root, "deploy/buildkit/seccomp-rootless-buildkit.json"),
		node+":/var/lib/kubelet/seccomp/profiles/launchway-rootless-buildkit.json").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}

	// The shipped manifest, in a test namespace. A kind node inside
	// rootless Podman has only 65536 user IDs, so the builds' range is
	// moved inside it.
	manifest, err := os.ReadFile(filepath.Join(root, "deploy/buildkit/buildkitd.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`launchway-build(\s)`).ReplaceAllString(string(manifest), buildNamespace+"$1")
	m = strings.Replace(m, "user:100000:65536", "user:10000:55000", 1)
	m = strings.Replace(m, `requests: { cpu: "1", memory: 2Gi }`, `requests: { cpu: 100m, memory: 256Mi }`, 1)
	if !strings.Contains(m, "name: "+buildNamespace+"\n") || strings.Contains(m, buildNamespace+"-test") {
		t.Fatal("the manifest's namespace was not replaced")
	}
	k.run("", "delete", "namespace", buildNamespace, "--ignore-not-found", "--wait=true", "--timeout=120s")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("kubectl", "--context", ctxName, "-n", buildNamespace, "logs", "deploy/buildkitd", "--tail=40").CombinedOutput()
			t.Logf("buildkitd:\n%s", out)
		}
		_ = exec.Command("kubectl", "--context", ctxName, "delete", "namespace", buildNamespace, "--wait=false").Run()
	})
	k.run(m, "apply", "-f", "-") // its pod waits for the Secret, made next
	ca := imagetest.NewCA(t)
	crt, key := ca.Issue(t, "buildkitd", false, "buildkitd", "buildkitd."+buildNamespace+".svc", "127.0.0.1")
	cliCrt, cliKey := ca.Issue(t, "runner", true)
	k.run("", "-n", buildNamespace, "create", "secret", "generic", "buildkitd-tls",
		"--from-file=ca.crt="+filepath.Join(ca.Dir, "ca.pem"), "--from-file=tls.crt="+crt, "--from-file=tls.key="+key)

	// A registry with a password, in the cluster.
	password := "pw-" + fmt.Sprint(time.Now().UnixNano())
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	k.run("", "-n", buildNamespace, "create", "secret", "generic", "registry-auth", "--from-literal=htpasswd=builder:"+string(hash))
	k.run(registryManifest, "-n", buildNamespace, "apply", "-f", "-")
	k.run("", "-n", buildNamespace, "rollout", "status", "deploy/buildkitd", "--timeout=300s")
	k.run("", "-n", buildNamespace, "rollout", "status", "deploy/registry", "--timeout=300s")
	// What the daemon's pod was admitted as: the relaxation, and no more.
	spec := k.run("", "-n", buildNamespace, "get", "pods", "-l", "app.kubernetes.io/name=buildkitd", "-o",
		"jsonpath={.items[0].spec.securityContext.seccompProfile.type} {.items[0].spec.securityContext.runAsUser} {.items[0].spec.containers[0].securityContext.capabilities.add}")
	if spec != `Localhost 1000 ["SETUID","SETGID"]` {
		t.Fatalf("buildkitd's pod: %s", spec)
	}
	bkPort := k.portForward(buildNamespace, "svc/buildkitd", 1234)
	regPort := k.portForward(buildNamespace, "svc/registry", 5000)
	regHost := fmt.Sprintf("127.0.0.1:%d", regPort)

	e := setupStore(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry"), []byte(password), 0o600); err != nil {
		t.Fatal(err)
	}
	stores := secrets.NewResolver()
	stores.Add("files", secrets.File{Dir: dir}, nil, nil)
	ex, err := executor.NewKubernetes(executor.KubernetesOptions{Context: ctxName, Namespace: "launchway-test", StartTimeout: "5m"},
		fmt.Sprintf("test-images-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	r := e.newRunner("k8s-builder")
	r.Exec, r.Secrets = ex, stores
	r.Images = &imagebuild.Config{
		Builders: []imagebuild.BuilderConfig{{Name: "cluster", Address: fmt.Sprintf("tcp://127.0.0.1:%d", bkPort), CAFile: filepath.Join(ca.Dir, "ca.pem"),
			CertFile: cliCrt, KeyFile: cliKey, ServerName: "buildkitd"}},
		Registries: map[string]imagebuild.RegistryConfig{"cluster": {Host: regHost, Insecure: true,
			Credentials: &imagebuild.Credentials{Username: "builder", Password: pipeline.SecretRef{Store: "files", Key: "registry"}},
			Push:        []imagebuild.PushRule{{Projects: []string{"demo"}, Repositories: []string{"team/**"}}}}},
	}
	e.startRunner(t, r)
	stamp := fmt.Sprint(time.Now().UnixNano())
	id := e.start(t, `
pipeline: demo
steps:
  prepare:
    image: docker.io/library/alpine:3.22
    run: |
      mkdir -p app
      echo "made in the pod `+stamp+` as $(id -u)" > app/greeting.txt
      printf 'FROM docker.io/library/alpine:3.22\nCOPY greeting.txt /greeting.txt\nRUN adduser -D app && chown app /greeting.txt\nUSER app\n' > app/Dockerfile
  image:
    kind: image
    needs: prepare
    context: app
    platforms: [linux/amd64]
    tags: [{ registry: cluster, repository: team/k8s, tag: { run: number } }]
    sbom: true
  smoke:
    image: docker.io/library/alpine:3.22
    needs: image
    run: echo "built $LAUNCHWAY_NEEDS_IMAGE_IMAGE"
`)
	d := e.wait(t, id, 20*time.Minute)
	logs := stepLogs(t, e, id, "image", 1)
	if d.Status != "succeeded" {
		for _, s := range d.StepList {
			t.Logf("%s %s %s", s.ID, s.Status, s.Error)
		}
		t.Fatalf("run %s\n%s", d.Status, logs)
	}
	if !strings.Contains(logs, "Copied the build context app out of the run's pod") {
		t.Fatalf("the context did not come from the pod:\n%s", logs)
	}
	imgs, err := e.eng.RunImages(id)
	if err != nil || len(imgs) != 1 || !imgs[0].Pushed || !imgs[0].SBOM {
		t.Fatalf("%v %+v", err, imgs)
	}
	ref, _ := name.ParseReference(imgs[0].Image, name.Insecure)
	img, err := remote.Image(ref, remote.WithAuth(authn.FromConfig(authn.AuthConfig{Username: "builder", Password: password})))
	if err != nil {
		t.Fatal(err)
	}
	if dg, _ := img.Digest(); "sha256:"+dg.Hex != imgs[0].Platforms[0].Digest {
		t.Fatalf("pulled %s, the step says %+v", dg, imgs[0].Platforms)
	}
	layers, _ := img.Layers()
	found := false
	for _, l := range layers {
		rc, _ := l.Uncompressed()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(rc)
		rc.Close()
		found = found || bytes.Contains(buf.Bytes(), []byte("made in the pod "+stamp+" as 1000"))
	}
	if !found {
		t.Fatal("the image does not hold the file the step wrote in the pod (as user 1000)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_ = ex.Cleanup(ctx, func(context.Context, []string) (map[string]executor.RunInfo, error) { return nil, nil })
	_ = engine.RunSucceeded
}

// registryManifest is a registry with a password, in the build namespace.
const registryManifest = `
apiVersion: apps/v1
kind: Deployment
metadata: { name: registry }
spec:
  replicas: 1
  selector: { matchLabels: { app: registry } }
  template:
    metadata: { labels: { app: registry } }
    spec:
      containers:
        - name: registry
          image: ` + imagetest.RegistryImage + `
          env:
            - { name: REGISTRY_AUTH, value: htpasswd }
            - { name: REGISTRY_AUTH_HTPASSWD_REALM, value: test }
            - { name: REGISTRY_AUTH_HTPASSWD_PATH, value: /auth/htpasswd }
          ports: [{ containerPort: 5000 }]
          readinessProbe: { tcpSocket: { port: 5000 } }
          volumeMounts: [{ name: auth, mountPath: /auth, readOnly: true }]
      volumes: [{ name: auth, secret: { secretName: registry-auth } }]
---
apiVersion: v1
kind: Service
metadata: { name: registry }
spec:
  selector: { app: registry }
  ports: [{ port: 5000, targetPort: 5000 }]
`
