package fleet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// These tests run a real fleet: four systemd containers standing in for
// virtual machines (sshd, a service under systemd), and HAProxy in front of
// them, all rootless under podman on a private network. They send real
// requests throughout every update and fail if one fails.
//
// They do not run by default: set LW_VMTEST=1 (and have podman and the
// image built by `podman build -t localhost/lw-vmtest-host:1 testdata/host`).
// They are not part of make check.

const (
	hostImage = "localhost/lw-vmtest-host:1"
	netName   = "lw-vmtest-net"
)

func podman(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("podman", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("podman %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func needVM(t *testing.T) {
	t.Helper()
	if os.Getenv("LW_VMTEST") != "1" {
		t.Skip("set LW_VMTEST=1 to run the fleet tests on systemd containers (they need podman and the image in testdata/host)")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not installed")
	}
	if err := exec.Command("podman", "image", "exists", hostImage).Run(); err != nil {
		t.Skipf("the host image %s is not built: podman build -t %s internal/fleet/testdata/host", hostImage, hostImage)
	}
}

type lab struct {
	pem    []byte
	hosts  []Host
	remote *SSH
	lb     *HAProxy
	lbURL  string
	apps   map[string][]byte // release -> binary
}

func buildApps(t *testing.T, dir string) map[string][]byte {
	apps := map[string][]byte{}
	for rel, mode := range map[string]string{"v1": "good", "v2": "good", "v3": "bad500", "v4": "badhealth"} {
		out := filepath.Join(dir, rel)
		cmd := exec.Command("go", "build", "-o", out, "-ldflags", fmt.Sprintf("-X main.version=%s -X main.mode=%s", rel, mode), ".")
		cmd.Dir = "testdata/app"
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", rel, err, b)
		}
		b, _ := os.ReadFile(out)
		apps[rel] = b
	}
	return apps
}

func newLab(t *testing.T, n int) *lab {
	needVM(t)
	cleanup := func() {
		for _, c := range strings.Fields(podman(t, "ps", "-a", "--format", "{{.Names}}", "--filter", "name=lw-vmtest-")) {
			_ = exec.Command("podman", "rm", "-f", "-t", "0", c).Run()
		}
		_ = exec.Command("podman", "network", "rm", "-f", netName).Run()
	}
	cleanup()
	t.Cleanup(cleanup)
	podman(t, "network", "create", netName)

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	pub := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))

	blk, _ := ssh.MarshalPrivateKey(priv, "")
	l := &lab{pem: pem.EncodeToMemory(blk), remote: &SSH{Signer: signer}, apps: buildApps(t, t.TempDir())}
	var servers []string
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("lw-vmtest-h%d", i)
		podman(t, "run", "-d", "--name", name, "--network", netName, "--systemd=always", "--memory", "256m",
			"-p", "127.0.0.1::22", "-p", "127.0.0.1::8080", hostImage)
		cmd := exec.Command("podman", "exec", "-i", name, "sh", "-c", "cat > /home/deploy/.ssh/authorized_keys && chown deploy:deploy /home/deploy/.ssh/authorized_keys && chmod 600 /home/deploy/.ssh/authorized_keys")
		cmd.Stdin = strings.NewReader(pub)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("authorizing the key: %v %s", err, b)
		}
		hk := podman(t, "exec", name, "cat", "/etc/ssh/ssh_host_ed25519_key.pub")
		sshAddr := podman(t, "port", name, "22/tcp")
		svcAddr := podman(t, "port", name, "8080/tcp")
		h := Host{Name: fmt.Sprintf("web-%d", i), Address: lines(sshAddr), HostKey: hk, Service: lines(svcAddr), Member: fmt.Sprintf("app/web-%d", i), User: "deploy"}
		l.hosts = append(l.hosts, h)
		servers = append(servers, fmt.Sprintf("  server web-%d %s:8080 check inter 200ms fall 2 rise 2", i, name))
	}
	cfg := "global\n  stats socket ipv4@0.0.0.0:9999 level admin\ndefaults\n  mode http\n  retries 0\n  timeout connect 2s\n  timeout client 30s\n  timeout server 30s\n  option httpchk GET /healthz\nfrontend fe\n  bind :8000\n  default_backend app\nbackend app\n  balance roundrobin\n" + strings.Join(servers, "\n") + "\n"
	cfgFile := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(cfgFile, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	podman(t, "run", "-d", "--name", "lw-vmtest-lb", "--network", netName, "-p", "127.0.0.1::8000", "-p", "127.0.0.1::9999",
		"-v", cfgFile+":/usr/local/etc/haproxy/haproxy.cfg:ro,z", "docker.io/library/haproxy:3.0-alpine")
	l.lb = &HAProxy{Network: "tcp", Address: lines(podman(t, "port", "lw-vmtest-lb", "9999/tcp"))}
	l.lbURL = "http://" + lines(podman(t, "port", "lw-vmtest-lb", "8000/tcp"))
	return l
}

func lines(s string) string { return strings.Fields(strings.ReplaceAll(s, "\n", " "))[0] }

func (l *lab) fleet(t *testing.T, audit *[]AuditEntry) *Fleet {
	lay, _ := NewLayout("web")
	var mu sync.Mutex
	rem := &Audited{Remote: l.remote, RunID: "itest", Sink: func(e AuditEntry) { mu.Lock(); *audit = append(*audit, e); mu.Unlock() }}
	chk := &HTTPCheck{Check: pipeline.HealthCheck{URL: "http://" + pipeline.FleetHostName + "/healthz"}}
	f := &Fleet{Remote: rem, Traffic: l.lb, Check: chk, Layout: lay,
		CheckURL: func(h Host) string { return chk.URLFor(h.Service) },
		Events:   func(e Event) { t.Logf("%-6s %-10s %s", e.Host, e.Phase, e.Message) }}
	return f
}

// install the first release on every host and wait until the balancer is
// serving all of them.
func (l *lab) bootstrap(t *testing.T, f *Fleet, rel string) {
	ctx := context.Background()
	deadline := time.Now().Add(90 * time.Second)
	for _, h := range l.hosts {
		for {
			if _, err := l.remote.Run(ctx, h, "true", nil); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("host %s never answered over ssh", h.Name)
			}
			time.Sleep(time.Second)
		}
		if err := f.install(ctx, h, rel, l.apps[rel]); err != nil {
			t.Fatal(err)
		}
		if err := f.restart(ctx, h); err != nil {
			t.Fatal(err)
		}
		if err := waitHealthy(ctx, f.Check, f.CheckURL(h), 2, 200*time.Millisecond, 60*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range l.hosts {
		if err := f.waitMember(ctx, h.Member, "up", 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
}

// loadgen sends requests through the balancer until stopped, counting.
type loadgen struct {
	sent, failed atomic.Int64
	byVersion    sync.Map
	stop         chan struct{}
	wg           sync.WaitGroup
}

func startLoad(url string, workers int) *loadgen {
	g := &loadgen{stop: make(chan struct{})}
	tr := &http.Transport{MaxIdleConnsPerHost: workers}
	cl := &http.Client{Timeout: 5 * time.Second, Transport: tr}
	for i := 0; i < workers; i++ {
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			for {
				select {
				case <-g.stop:
					return
				default:
				}
				g.sent.Add(1)
				resp, err := cl.Get(url + "/")
				if err != nil {
					g.failed.Add(1)
				} else {
					v := resp.Header.Get("X-Version")
					resp.Body.Close()
					if resp.StatusCode != 200 {
						g.failed.Add(1)
					} else {
						n, _ := g.byVersion.LoadOrStore(v, new(atomic.Int64))
						n.(*atomic.Int64).Add(1)
					}
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}
	return g
}

func (g *loadgen) finish() (sent, failed int64) {
	close(g.stop)
	g.wg.Wait()
	return g.sent.Load(), g.failed.Load()
}

func (g *loadgen) served() string {
	var parts []string
	g.byVersion.Range(func(k, v any) bool {
		parts = append(parts, fmt.Sprintf("%s=%d", k, v.(*atomic.Int64).Load()))
		return true
	})
	return strings.Join(parts, " ")
}

func TestVMRollingUpdateHasZeroFailedRequests(t *testing.T) {
	l := newLab(t, 4)
	var audit []AuditEntry
	f := l.fleet(t, &audit)
	l.bootstrap(t, f, "v1")
	g := startLoad(l.lbURL, 8)
	time.Sleep(500 * time.Millisecond)
	rep, err := f.Roll(context.Background(), l.hosts, RollSpec{Release: "v2", Artifact: l.apps["v2"], MaxUnavailable: 1, DrainMax: 20 * time.Second, Bake: 500 * time.Millisecond})
	time.Sleep(500 * time.Millisecond)
	sent, failed := g.finish()
	t.Logf("MEASURED good rolling update: sent %d, failed %d, served %s, updated %v, drain timeouts %v", sent, failed, g.served(), rep.Updated, rep.DrainTimeouts)
	if err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("%d of %d requests failed during the update", failed, sent)
	}
	if sent < 500 {
		t.Fatalf("only %d requests were sent: the test proves nothing", sent)
	}
	checkAudit(t, audit)
}

func TestVMRollingBadReleaseReturnsWithZeroFailedRequests(t *testing.T) {
	l := newLab(t, 4)
	var audit []AuditEntry
	f := l.fleet(t, &audit)
	l.bootstrap(t, f, "v1")
	g := startLoad(l.lbURL, 8)
	time.Sleep(500 * time.Millisecond)
	// v4 fails its health check: the first host must never take traffic.
	rep, err := f.Roll(context.Background(), l.hosts, RollSpec{Release: "v4", Artifact: l.apps["v4"], MaxUnavailable: 1, DrainMax: 20 * time.Second,
		CheckWithin: 5 * time.Second})
	time.Sleep(500 * time.Millisecond)
	sent, failed := g.finish()
	t.Logf("MEASURED bad release (health fails), rolling: sent %d, failed %d, served %s, returned %v quarantined %v", sent, failed, g.served(), rep.Returned, rep.Quarantined)
	if err == nil {
		t.Fatal("a release that fails its health check was accepted")
	}
	if failed != 0 || sent < 500 {
		t.Fatalf("%d of %d requests failed", failed, sent)
	}
	for _, h := range l.hosts {
		cur, _ := f.currentRelease(context.Background(), h)
		if cur != "v1" {
			t.Fatalf("%s is on %s after the return", h.Name, cur)
		}
	}
}

// probe sends requests through the balancer for a while and fails if any
// fails: the verification between Pathfinder steps (the same call a check:
// makes, made at a rate that finds even a small share's failures).
func probe(url string, d time.Duration) error {
	cl := &http.Client{Timeout: 3 * time.Second}
	end := time.Now().Add(d)
	var n, bad int
	for time.Now().Before(end) {
		n++
		resp, err := cl.Get(url + "/")
		if err != nil || resp.StatusCode != 200 {
			bad++
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(8 * time.Millisecond)
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d requests through the balancer failed", bad, n)
	}
	return nil
}

func TestVMPathfinderGoodReleaseHasZeroFailedRequests(t *testing.T) {
	l := newLab(t, 4)
	var audit []AuditEntry
	f := l.fleet(t, &audit)
	l.bootstrap(t, f, "v1")
	g := startLoad(l.lbURL, 8)
	time.Sleep(500 * time.Millisecond)
	rep, err := f.Pathfinder(context.Background(), l.hosts[:2], l.hosts[2:], PathfinderSpec{Release: "v2", Artifact: l.apps["v2"], DrainMax: 20 * time.Second,
		Bake: 300 * time.Millisecond, Watch: time.Second, Verify: func(_ context.Context, p int) error { return probe(l.lbURL, 1500*time.Millisecond) }})
	time.Sleep(300 * time.Millisecond)
	sent, failed := g.finish()
	t.Logf("MEASURED good Pathfinder 10/50/100: sent %d, failed %d, served %s, reached %d%%", sent, failed, g.served(), rep.Reached)
	if err != nil {
		t.Fatal(err)
	}
	if failed != 0 || sent < 500 {
		t.Fatalf("%d of %d requests failed", failed, sent)
	}
}

func TestVMPathfinderBadReleaseIsBoundedByItsShareThenZero(t *testing.T) {
	l := newLab(t, 4)
	var audit []AuditEntry
	f := l.fleet(t, &audit)
	l.bootstrap(t, f, "v1")
	g := startLoad(l.lbURL, 8)
	time.Sleep(500 * time.Millisecond)
	var stepSent, stepFailed int64
	var mu sync.Mutex
	// v3 is healthy (/healthz answers) but fails real requests.
	rep, err := f.Pathfinder(context.Background(), l.hosts[:2], l.hosts[2:], PathfinderSpec{Release: "v3", Artifact: l.apps["v3"], DrainMax: 20 * time.Second,
		Bake: 200 * time.Millisecond, Verify: func(_ context.Context, p int) error {
			s0, f0 := g.sent.Load(), g.failed.Load()
			e := probe(l.lbURL, 1500*time.Millisecond)
			mu.Lock()
			stepSent, stepFailed = g.sent.Load()-s0, g.failed.Load()-f0
			mu.Unlock()
			return e
		}})
	if !errors.Is(err, ErrPathfinderReturned) {
		t.Fatalf("err = %v", err)
	}
	// After the return: a quiet second of traffic must have no failures.
	time.Sleep(300 * time.Millisecond)
	s1, f1 := g.sent.Load(), g.failed.Load()
	time.Sleep(2 * time.Second)
	afterSent, afterFailed := g.sent.Load()-s1, g.failed.Load()-f1
	sent, failed := g.finish()
	t.Logf("MEASURED bad Pathfinder (10%% step, then weights back): sent %d, failed %d (%.2f%%); in the 10%% step window: sent %d failed %d (%.1f%%); after the return: sent %d failed %d; reached %d%% returned=%v",
		sent, failed, 100*float64(failed)/float64(sent), stepSent, stepFailed, 100*float64(stepFailed)/float64(max(stepSent, 1)), afterSent, afterFailed, rep.Reached, rep.Returned)
	if afterFailed != 0 || afterSent < 200 {
		t.Fatalf("after the return, %d of %d requests failed", afterFailed, afterSent)
	}
	if share := float64(stepFailed) / float64(max(stepSent, 1)); share > 0.15 {
		t.Fatalf("the failure share in the 10%% step was %.1f%%, above the Pathfinder's share plus slack", share*100)
	}
	if failed == 0 {
		t.Fatal("the bad release was never hit: the test proves nothing")
	}
}

// checkAudit: every command on every host was recorded before and after.
func checkAudit(t *testing.T, a []AuditEntry) {
	t.Helper()
	var start, done int
	for _, e := range a {
		if e.Done {
			done++
		} else {
			start++
		}
		if strings.Contains(e.Command, "BEGIN OPENSSH") {
			t.Fatal("a key appears in an audited command")
		}
	}
	if start == 0 || start != done {
		t.Fatalf("audit has %d starts and %d ends", start, done)
	}
}

// The Station's own command, end to end: launchway fleet run reads the
// pipeline's fleet: block and the inventory, takes the key from a secret
// store by reference, and does the rolling update with the balancer, while
// requests flow. A bad release returns every host and the command fails.
func TestVMStationCommandEndToEnd(t *testing.T) {
	l := newLab(t, 4)
	var audit []AuditEntry
	f := l.fleet(t, &audit)
	l.bootstrap(t, f, "v1")
	dir := t.TempDir()
	bin := filepath.Join(dir, "launchway")
	build := exec.Command("go", "build", "-o", bin, "./cmd/launchway")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var inv strings.Builder
	inv.WriteString("hosts:\n")
	for _, h := range l.hosts {
		fmt.Fprintf(&inv, "  - { name: %s, address: %q, user: deploy, host_key: %q, service: %q, member: %s }\n", h.Name, h.Address, strings.TrimSpace(h.HostKey), h.Service, h.Member)
	}
	pl := fmt.Sprintf(`pipeline: web
steps:
  deploy-prod:
    kind: deploy
    environment: prod
    service: web
    fleet:
      artifact: dist/web
      max_unavailable: 1
      drain: 20s
      check: { url: /healthz }
      traffic: { provider: haproxy, admin: "tcp:%s" }
      ssh: { key: { store: env, key: LW_FLEET_KEY } }
`, l.lb.Address)
	for name, content := range map[string]string{"inventory.yaml": inv.String(), "pipeline.yaml": pl} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(rel string, art []byte) (string, error) {
		_ = os.MkdirAll(filepath.Join(dir, "dist"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, "dist", "web"), art, 0o755)
		cmd := exec.Command(bin, "fleet", "run", "--file", filepath.Join(dir, "pipeline.yaml"), "--step", "deploy-prod",
			"--inventory", filepath.Join(dir, "inventory.yaml"), "--release", rel, "--workspace", dir,
			"--audit", filepath.Join(dir, "audit-"+rel+".jsonl"), "--events", filepath.Join(dir, "events-"+rel+".jsonl"))
		cmd.Env = append(os.Environ(), "LW_FLEET_KEY="+string(l.pem))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	g := startLoad(l.lbURL, 8)
	time.Sleep(300 * time.Millisecond)
	out, err := run("v2", l.apps["v2"])
	if err != nil {
		t.Fatalf("good release: %v\n%s", err, out)
	}
	out4, err4 := run("v4", l.apps["v4"])
	time.Sleep(300 * time.Millisecond)
	sent, failed := g.finish()
	t.Logf("MEASURED launchway fleet run, good v2 then bad v4 (returned): sent %d, failed %d, served %s", sent, failed, g.served())
	if err4 == nil {
		t.Fatal("the bad release succeeded")
	}
	if !strings.Contains(out4, "returned") && !strings.Contains(out4, "return") {
		t.Fatalf("no return in the output:\n%s", out4)
	}
	if failed != 0 || sent < 500 {
		t.Fatalf("%d of %d requests failed", failed, sent)
	}
	au, _ := os.ReadFile(filepath.Join(dir, "audit-v2.jsonl"))
	if strings.Count(string(au), `"done":true`) < 20 || strings.Contains(string(au), "OPENSSH PRIVATE KEY") {
		t.Fatalf("the audit file is wrong:\n%s", au)
	}
	for _, h := range l.hosts {
		if cur, _ := f.currentRelease(context.Background(), h); cur != "v2" {
			t.Fatalf("%s is on %s", h.Name, cur)
		}
	}
}
