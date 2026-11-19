// Package imagetest starts what image step tests need, in Podman or
// Docker: a rootless BuildKit daemon reached over mutual TLS, and
// registries with and without authentication and TLS. It is for tests
// only. Tests that use it run when LAUNCHWAY_TEST_CONTAINERS is set.
package imagetest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Pinned images.
const (
	BuildKitImage = "docker.io/moby/buildkit:v0.35.0-rootless@sha256:5987568f92dafbfb679879de59c1cd8d26597f5f6635c314e166164e2615870c"
	RegistryImage = "docker.io/library/registry:2.8.3@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"
	CosignImage   = "gcr.io/projectsigstore/cosign:v2.6.1@sha256:68839b7f13dac5a6744a5d8818e984dd39183374e37855c19e14d623d9bc9037"
	// Cosign3Image is cosign 3, to check that what Launchway writes still
	// verifies with the newer release.
	Cosign3Image = "ghcr.io/sigstore/cosign/cosign:v3.0.2@sha256:b29487e48205d875c324c79583e2806d9d269c0fa299e0861bbec023d8430c8b"
)

// Skip skips the test unless container tests are asked for.
func Skip(t testing.TB) {
	t.Helper()
	if testing.Short() || os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
}

// Engine is podman or docker.
func Engine(t testing.TB) string {
	t.Helper()
	for _, e := range []string{"podman", "docker"} {
		if p, err := exec.LookPath(e); err == nil {
			return p
		}
	}
	t.Fatal("neither podman nor docker is installed")
	return ""
}

// Run runs the container engine and returns its output.
func Run(t testing.TB, args ...string) string {
	t.Helper()
	out, err := exec.Command(Engine(t), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", filepath.Base(Engine(t)), strings.Join(args, " "), err, out)
	}
	return string(out)
}

// FreePort returns a TCP port free on 127.0.0.1 a moment ago.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// CA is a test certificate authority.
type CA struct {
	Dir  string // holds ca.pem
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// NewCA makes a CA in a new directory.
func NewCA(t testing.TB) *CA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "launchway test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", der)
	return &CA{Dir: dir, cert: cert, key: key}
}

// Issue writes name.pem and name.key, a certificate for the hosts given
// (DNS names or IP addresses), for a server or a client.
func (ca *CA) Issue(t testing.TB, name string, client bool, hosts ...string) (certFile, keyFile string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if client {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalPKCS8PrivateKey(key)
	certFile, keyFile = filepath.Join(ca.Dir, name+".pem"), filepath.Join(ca.Dir, name+".key")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "PRIVATE KEY", kder)
	// The daemons in containers run as other users.
	_ = os.Chmod(keyFile, 0o644)
	return certFile, keyFile
}

func writePEM(t testing.TB, path, typ string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Registry is a registry:2 container on 127.0.0.1.
type Registry struct {
	Host     string // 127.0.0.1:port
	User     string // empty without authentication
	Password string
	CAFile   string // empty without TLS
	Name     string // the container
}

// RegistryOptions choose authentication and TLS.
type RegistryOptions struct {
	Auth bool
	TLS  *CA
}

// StartRegistry starts a registry, removed when the test ends.
func StartRegistry(t testing.TB, o RegistryOptions) *Registry {
	t.Helper()
	port := FreePort(t)
	r := &Registry{Host: fmt.Sprintf("127.0.0.1:%d", port), Name: fmt.Sprintf("launchway-test-registry-%d", port)}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0o755)
	args := []string{"run", "-d", "--rm", "--name", r.Name, "-p", fmt.Sprintf("127.0.0.1:%d:5000", port),
		"-v", dir + ":/conf:ro,Z", "-e", "REGISTRY_STORAGE_DELETE_ENABLED=true"}
	if o.Auth {
		r.User = "builder"
		r.Password = "pw-" + randHex(12)
		h, err := bcrypt.GenerateFromPassword([]byte(r.Password), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "htpasswd"), []byte(r.User+":"+string(h)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-e", "REGISTRY_AUTH=htpasswd", "-e", "REGISTRY_AUTH_HTPASSWD_REALM=test",
			"-e", "REGISTRY_AUTH_HTPASSWD_PATH=/conf/htpasswd")
	}
	if o.TLS != nil {
		cert, key := o.TLS.Issue(t, fmt.Sprintf("registry-%d", port), false, "127.0.0.1", "localhost")
		for _, f := range []string{cert, key} {
			data, _ := os.ReadFile(f)
			_ = os.WriteFile(filepath.Join(dir, filepath.Base(f)), data, 0o644)
		}
		args = append(args, "-e", "REGISTRY_HTTP_TLS_CERTIFICATE=/conf/"+filepath.Base(cert),
			"-e", "REGISTRY_HTTP_TLS_KEY=/conf/"+filepath.Base(key))
		r.CAFile = filepath.Join(o.TLS.Dir, "ca.pem")
	}
	Run(t, append(args, RegistryImage)...)
	t.Cleanup(func() { _ = exec.Command(Engine(t), "rm", "-f", r.Name).Run() })
	waitTCP(t, r.Host)
	return r
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func waitTCP(t testing.TB, addr string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			c.Close()
			time.Sleep(300 * time.Millisecond)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s did not start listening", addr)
}

// BuildKit is a rootless BuildKit daemon in a container, on the host's
// network (so that it reaches registries on 127.0.0.1 as the runner does),
// listening with mutual TLS.
type BuildKit struct {
	Address           string // tcp://127.0.0.1:port
	CAFile            string
	CertFile, KeyFile string // the client's
	Name              string
	ServerName        string
}

// BuildKitOptions configure the daemon.
type BuildKitOptions struct {
	// Registries are hosts it talks to: plain HTTP ones, and ones whose TLS
	// certificate a CA file signed.
	HTTP []string
	CAs  map[string]string // host → CA file
	// Platforms, if set, limits the daemon's worker to these.
	Platforms []string
}

var seccompOnce sync.Once

// StartBuildKit starts a daemon, removed when the test ends. Its state
// lives in a volume kept between test runs, so base images and the
// scanner's database are downloaded once.
func StartBuildKit(t testing.TB, o BuildKitOptions) *BuildKit {
	t.Helper()
	ca := NewCA(t)
	port := FreePort(t)
	ca.Issue(t, "buildkitd", false, "127.0.0.1", "buildkitd") // buildkitd.pem and buildkitd.key, for the daemon
	cliCert, cliKey := ca.Issue(t, "runner", true)
	b := &BuildKit{Address: fmt.Sprintf("tcp://127.0.0.1:%d", port), CAFile: filepath.Join(ca.Dir, "ca.pem"),
		CertFile: cliCert, KeyFile: cliKey, Name: fmt.Sprintf("launchway-test-buildkitd-%d", port), ServerName: "buildkitd"}
	var toml bytes.Buffer
	if len(o.Platforms) > 0 {
		fmt.Fprintf(&toml, "[worker.oci]\n  platforms = [%s]\n", quoteAll(o.Platforms))
	}
	for _, h := range o.HTTP {
		fmt.Fprintf(&toml, "[registry.%q]\n  http = true\n", h)
	}
	for h, caFile := range o.CAs {
		data, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatal(err)
		}
		name := "reg-" + strings.NewReplacer(":", "-", ".", "-").Replace(h) + ".pem"
		_ = os.WriteFile(filepath.Join(ca.Dir, name), data, 0o644)
		fmt.Fprintf(&toml, "[registry.%q]\n  http = false\n  ca = [\"/certs/%s\"]\n", h, name)
	}
	if err := os.WriteFile(filepath.Join(ca.Dir, "buildkitd.toml"), toml.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	subid := filepath.Join(ca.Dir, "subid")
	_ = os.WriteFile(subid, []byte("user:10000:55000\n"), 0o644)
	seccomp := filepath.Join(ca.Dir, "seccomp.json")
	if err := os.WriteFile(seccomp, RootlessBuildKitSeccomp(t), 0o644); err != nil {
		t.Fatal(err)
	}
	// Two daemons never share a state directory.
	volume := "launchway-test-buildkit"
	if len(o.Platforms) > 0 {
		volume += "-" + strings.NewReplacer("/", "-", ",", "-").Replace(strings.Join(o.Platforms, "-"))
	}
	Run(t, "run", "-d", "--rm", "--name", b.Name, "--network", "host",
		"--security-opt", "seccomp="+seccomp, "--security-opt", "apparmor=unconfined", "--security-opt", "label=disable",
		"-v", ca.Dir+":/certs:ro,Z", "-v", subid+":/etc/subuid:ro,Z", "-v", subid+":/etc/subgid:ro,Z",
		"-v", volume+":/home/user/.local/share/buildkit",
		BuildKitImage, "--oci-worker-no-process-sandbox", "--config", "/certs/buildkitd.toml",
		"--addr", fmt.Sprintf("tcp://127.0.0.1:%d", port),
		"--tlscacert", "/certs/ca.pem", "--tlscert", "/certs/buildkitd.pem", "--tlskey", "/certs/buildkitd.key")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command(Engine(t), "logs", "--tail", "40", b.Name).CombinedOutput()
			t.Logf("buildkitd log:\n%s", out)
		}
		_ = exec.Command(Engine(t), "rm", "-f", b.Name).Run()
	})
	waitTCP(t, fmt.Sprintf("127.0.0.1:%d", port))
	return b
}

func quoteAll(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}

// Cosign runs cosign (image, one of CosignImage and Cosign3Image) on the
// host's network with a directory mounted at /w, and returns its output
// and whether it succeeded.
func Cosign(t testing.TB, image, dir string, args ...string) (string, bool) {
	t.Helper()
	all := append([]string{"run", "--rm", "--user", "0", "--network", "host", "-v", dir + ":/w:Z", "-e", "COSIGN_PASSWORD", image}, args...)
	cmd := exec.Command(Engine(t), all...)
	cmd.Env = append(os.Environ(), "COSIGN_PASSWORD="+os.Getenv("LAUNCHWAY_TEST_COSIGN_PASSWORD"))
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// RootlessBuildKitSeccomp returns deploy/buildkit/seccomp-rootless-buildkit.json:
// the container runtime's default profile, plus the system calls rootless
// BuildKit needs to make user namespaces.
func RootlessBuildKitSeccomp(t testing.TB) []byte {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("cannot find the repository's root")
		}
		dir = parent
	}
	data, err := os.ReadFile(filepath.Join(dir, "deploy", "buildkit", "seccomp-rootless-buildkit.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
