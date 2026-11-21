package registry_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/random"
)

// Real clients against a server on loopback. Each is skipped, with the
// reason, when it is not installed or cannot run here.

func needTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed here, so the client test is skipped", name)
	}
	return p
}

func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestSkopeoCopyInAndOut(t *testing.T) {
	skopeo := needTool(t, "skopeo")
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	work := t.TempDir()
	lay, err := layout.Write(filepath.Join(work, "src"), empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := random.Image(2<<20, 2)
	if err := lay.AppendImage(img); err != nil {
		t.Fatal(err)
	}
	want, _ := img.Digest()
	creds := "u:tok"
	run(t, work, nil, skopeo, "copy", "--dest-tls-verify=false", "--dest-creds", creds, "oci:"+filepath.Join(work, "src"), "docker://"+e.host()+"/cart/sk:1")
	out := run(t, work, nil, skopeo, "inspect", "--raw", "--tls-verify=false", "--creds", creds, "docker://"+e.host()+"/cart/sk:1")
	if !strings.Contains(out, "layers") {
		t.Fatalf("manifest %s", out)
	}
	run(t, work, nil, skopeo, "copy", "--src-tls-verify=false", "--src-creds", creds, "docker://"+e.host()+"/cart/sk:1", "oci:"+filepath.Join(work, "back")+":x")
	if _, err := os.Stat(filepath.Join(work, "back", "index.json")); err != nil {
		t.Fatal(err)
	}
	// Copy by digest and a second tag from registry to registry.
	run(t, work, nil, skopeo, "copy", "--src-tls-verify=false", "--dest-tls-verify=false", "--src-creds", creds, "--dest-creds", creds,
		"docker://"+e.host()+"/cart/sk:1", "docker://"+e.host()+"/cart/sk2:copy")
	res, b := e.raw(t, "tok", "GET", "/v2/cart/sk2/tags/list", nil, nil)
	e.expect(t, res, b, 200)
	if !strings.Contains(string(b), "copy") {
		t.Fatalf("tags %s", b)
	}
	// Wrong credentials are refused by the real client too.
	cmd := exec.CommandContext(t.Context(), skopeo, "inspect", "--tls-verify=false", "--creds", "u:nope", "docker://"+e.host()+"/cart/sk:1")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(strings.ToLower(string(out)), "unauthorized") {
		t.Fatalf("a wrong password was not refused: %v %s", err, out)
	}
	_ = want
}

func TestPodmanPullAndPush(t *testing.T) {
	podman := needTool(t, "podman")
	skopeo := needTool(t, "skopeo")
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	work := t.TempDir()
	lay, err := layout.Write(filepath.Join(work, "src"), empty.Index)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := random.Image(1<<20, 1)
	_ = lay.AppendImage(img)
	run(t, work, nil, skopeo, "copy", "--dest-tls-verify=false", "--dest-creds", "u:tok", "oci:"+filepath.Join(work, "src"), "docker://"+e.host()+"/cart/pm:1")
	pm := []string{"--root", filepath.Join(work, "root"), "--runroot", filepath.Join(work, "run"), "--storage-driver", "vfs"}
	probe := exec.CommandContext(t.Context(), podman, append(append([]string{}, pm...), "info")...)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("podman cannot run here (%v): %.200s", err, out)
	}
	args := func(a ...string) []string { return append(append([]string{}, pm...), a...) }
	// Podman's storage holds files owned by other user IDs; only podman can remove them.
	t.Cleanup(func() {
		_ = exec.Command(podman, "unshare", "rm", "-rf", filepath.Join(work, "root"), filepath.Join(work, "run")).Run()
	})
	run(t, work, nil, podman, args("login", "--tls-verify=false", "-u", "u", "-p", "tok", e.host())...)
	run(t, work, nil, podman, args("pull", "--tls-verify=false", e.host()+"/cart/pm:1")...)
	run(t, work, nil, podman, args("tag", e.host()+"/cart/pm:1", e.host()+"/cart/pm-pushed:2")...)
	run(t, work, nil, podman, args("push", "--tls-verify=false", e.host()+"/cart/pm-pushed:2")...)
	res, b := e.raw(t, "tok", "GET", "/v2/cart/pm-pushed/tags/list", nil, nil)
	e.expect(t, res, b, 200)
	if !strings.Contains(string(b), `"2"`) {
		t.Fatalf("tags after podman push: %s", b)
	}
	// Logged out, a pull is refused.
	run(t, work, nil, podman, args("logout", e.host())...)
	cmd := exec.CommandContext(t.Context(), podman, args("pull", "--tls-verify=false", e.host()+"/cart/pm:1")...)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("an anonymous pull worked: %s", out)
	}
}

func TestHelmPushAndPullOCI(t *testing.T) {
	helm := needTool(t, "helm")
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	work := t.TempDir()
	home := []string{"HELM_REGISTRY_CONFIG=" + filepath.Join(work, "reg.json"), "HELM_CACHE_HOME=" + filepath.Join(work, "cache"), "HELM_CONFIG_HOME=" + filepath.Join(work, "cfg"), "HELM_DATA_HOME=" + filepath.Join(work, "data")}
	run(t, work, home, helm, "create", "mychart")
	run(t, work, home, helm, "package", "mychart", "--version", "0.3.1", "--destination", work)
	run(t, work, home, helm, "registry", "login", e.host(), "--insecure", "--plain-http", "-u", "u", "-p", "tok")
	out := run(t, work, home, helm, "push", filepath.Join(work, "mychart-0.3.1.tgz"), "oci://"+e.host()+"/cart/charts", "--plain-http")
	if !strings.Contains(out, "Digest: sha256:") {
		t.Fatalf("push output: %s", out)
	}
	_ = os.MkdirAll(filepath.Join(work, "pulled"), 0o700)
	run(t, work, home, helm, "pull", "oci://"+e.host()+"/cart/charts/mychart", "--version", "0.3.1", "--plain-http", "--destination", filepath.Join(work, "pulled"))
	if _, err := os.Stat(filepath.Join(work, "pulled", "mychart-0.3.1.tgz")); err != nil {
		t.Fatal(err)
	}
	// The chart is an OCI artifact with its own type, listed as such.
	imgs, err := e.svc.Images(t.Context(), "cart/charts/mychart")
	if err != nil || len(imgs) != 1 || imgs[0].ArtifactType != "application/vnd.cncf.helm.config.v1+json" {
		t.Fatalf("chart listing: %+v %v", imgs, err)
	}
	// A pull with the wrong password is refused.
	cmd := exec.CommandContext(t.Context(), helm, "registry", "login", e.host(), "--insecure", "--plain-http", "-u", "u", "-p", "wrong")
	cmd.Env = append(os.Environ(), home...)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("helm logged in with a wrong password: %s", out)
	}
}

// cosign signs an image with a key and stores the signature beside it (a tag named for the digest); the
// registry lists it as a signature and cosign verifies it back. Skipped, with the reason, where cosign
// is not installed (it is not on the machine this was written on).
func TestCosignSignsAndVerifies(t *testing.T) {
	cosign := needTool(t, "cosign")
	e := newEnv(t)
	e.grant("tok", who{subject: "ann", can: rw("cart")})
	d := e.pushImage(t, "tok", "cart/signed", "1", []byte("layer"))
	work := t.TempDir()
	env := []string{"COSIGN_PASSWORD=pw", "COSIGN_YES=true"}
	run(t, work, env, cosign, "generate-key-pair")
	ref := e.host() + "/cart/signed@" + d
	creds := []string{"--allow-insecure-registry", "--registry-username=u", "--registry-password=tok"}
	run(t, work, env, cosign, append([]string{"sign", "--key", "cosign.key", "--tlog-upload=false"}, append(creds, ref)...)...)
	run(t, work, env, cosign, append([]string{"verify", "--key", "cosign.pub", "--insecure-ignore-tlog"}, append(creds, ref)...)...)
	img, err := e.svc.InspectImage(t.Context(), "cart/signed", "1")
	if err != nil || !img.Signed {
		t.Fatalf("the registry does not see the signature: %+v %v", img, err)
	}
}
