package ansible

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Ansible against real (container) hosts. The hosts are systemd
// containers named lw-cactest-N that this test starts and removes; the
// inventory REFUSES any other name, so a configuration tool can never be
// pointed at a real machine by this test. Needs podman and the two images
// (podman build -t localhost/lw-cactest-ee:1 packaging/ansible-ee;
// podman build -t localhost/lw-cactest-host:1 test/cac/host), and runs only
// with LAUNCHWAY_TEST_CONTAINERS=1.

const (
	eeImage   = "localhost/lw-cactest-ee:1"
	hostImage = "localhost/lw-cactest-host:1"
	canary    = "CANARY-s3cr3t-7f3a"
)

func podman(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("podman", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("podman %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func needContainers(t *testing.T) {
	t.Helper()
	if os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman is not installed")
	}
	for _, img := range []string{eeImage, hostImage} {
		if exec.Command("podman", "image", "exists", img).Run() != nil {
			t.Skipf("the image %s is not built (see the comment of this test)", img)
		}
	}
}

type lab struct {
	t       *testing.T
	hosts   []string
	ws, key string
	known   string
	net     string
}

// guard is the inventory's refusal: only lab hosts are ever targets.
func guard(hosts []string) error {
	for _, h := range hosts {
		if !strings.HasPrefix(h, "lw-cactest-") {
			return fmt.Errorf("refusing host %q: only lw-cactest-* containers are test targets", h)
		}
	}
	return nil
}

func newLab(t *testing.T, n int) *lab {
	needContainers(t)
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".cache", "cac-scratch", "tests")
	_ = os.MkdirAll(base, 0o755)
	dir, err := os.MkdirTemp(base, "ansible-")
	if err != nil {
		t.Fatal(err)
	}
	l := &lab{t: t, ws: filepath.Join(dir, "ws"), key: filepath.Join(dir, "id"), net: "lw-cactest-net"}
	_ = exec.Command("podman", "network", "create", l.net).Run()
	_ = os.MkdirAll(l.ws, 0o755)
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", l.key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(l.key + ".pub")
	for i := 1; i <= n; i++ {
		l.hosts = append(l.hosts, fmt.Sprintf("lw-cactest-%d", i))
	}
	if err := guard(l.hosts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, h := range l.hosts {
			_ = exec.Command("podman", "rm", "-f", h).Run()
		}
		_ = exec.Command("podman", "unshare", "rm", "-rf", dir).Run()
		_ = os.RemoveAll(dir)
	})
	for _, h := range l.hosts {
		_ = exec.Command("podman", "rm", "-f", h).Run()
		podman(t, "", "run", "-d", "--name", h, "--network", l.net, "--systemd=always", "--hostname", h, hostImage)
	}
	deadline := time.Now().Add(60 * time.Second)
	for _, h := range l.hosts {
		for {
			if exec.Command("podman", "exec", h, "systemctl", "is-system-running", "--wait").Run() == nil || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}
		podman(t, string(pub), "exec", "-i", h, "sh", "-c", "mkdir -p /root/.ssh; cat > /root/.ssh/authorized_keys; chmod 600 /root/.ssh/authorized_keys; ssh-keygen -A >/dev/null; systemctl reset-failed ssh; systemctl start ssh")
		l.known += podman(t, "", "run", "--rm", "--network", l.net, eeImage, "ssh-keyscan", "-t", "ed25519", h)
	}
	l.known = strings.Join(filterKnown(l.known), "\n")
	return l
}

func filterKnown(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.HasPrefix(ln, "lw-cactest-") {
			out = append(out, ln)
		}
	}
	return out
}

// play runs the library's script in the execution image against the lab.
func (l *lab) play(extra ...string) (*Report, string) {
	t := l.t
	t.Helper()
	fixture, _ := filepath.Abs("../../../test/cac/fixture")
	lib, _ := filepath.Abs("../../starters/library")
	if out, err := exec.Command("podman", "unshare", "rm", "-rf", l.ws).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if out, err := exec.Command("cp", "-r", fixture, l.ws).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	key, _ := os.ReadFile(l.key)
	args := []string{"run", "--rm", "--userns=keep-id:uid=1000,gid=1000", "--network", l.net, "-v", l.ws + ":/work:Z", "-v", lib + ":/lwlib:ro,Z", "-w", "/work",
		"-e", "LAUNCHWAY_LIB=/lwlib", "-e", "ANSIBLE_SSH_KEY=" + string(key), "-e", "PARAM_KNOWN_HOSTS=" + l.known,
		"-e", "PARAM_PLAYBOOK=site.yml", "-e", "PARAM_INVENTORY=inline", "-e", "PARAM_HOSTS=" + strings.Join(l.hosts, ","), "-e", "PARAM_USER=root",
		"-e", "PARAM_ENVIRONMENT=lab"}
	for _, e := range extra {
		args = append(args, "-e", e)
	}
	args = append(args, eeImage, "sh", "/lwlib/scripts/ansible/run.sh")
	cmd := exec.Command("podman", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	b, err := os.ReadFile(filepath.Join(l.ws, ".launchway/ansible/report.json"))
	if err != nil {
		t.Fatalf("no report (run error %v):\n%s", runErr, out.String())
	}
	r, err := Parse(b, canary)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil && r.Summary.Failed == 0 && r.Summary.Unreachable == 0 {
		t.Fatalf("the script failed but the report shows no failure: %v\n%s", runErr, out.String())
	}
	return r, out.String()
}

func TestRealAnsibleAgainstSystemdContainers(t *testing.T) {
	l := newLab(t, 2)
	vars := func(more string) string {
		return `PARAM_EXTRA_VARS={"demo_token":"` + canary + `"` + more + `}`
	}

	// 1. First run: installs a file and a service on both hosts.
	r, log := l.play("PARAM_DIFF=true", vars(""))
	if r.Summary.Changed != 2 || r.Summary.Failed != 0 {
		t.Fatalf("first run: %+v\n%s", r.Summary, log)
	}
	if !strings.Contains(l.exec(l.hosts[0], "cat", "/etc/lw-demo.conf"), "hello from launchway") || !strings.Contains(l.exec(l.hosts[1], "systemctl", "is-active", "lw-demo.service"), "active") {
		t.Fatal("the hosts were not configured")
	}
	hasDiff := false
	for _, p := range r.Plays {
		for _, tk := range p.Tasks {
			for _, res := range tk.Results {
				if len(res.Diff) > 0 && strings.Contains(res.Diff[0].Text, "message=hello from launchway") {
					hasDiff = true
				}
			}
		}
	}
	if !hasDiff {
		t.Fatal("diff mode produced no diff of the file")
	}

	// 2. Second run: idempotent, 0 changed.
	if r, log = l.play(vars("")); r.Summary.Changed != 0 || r.Summary.OK != 2 {
		t.Fatalf("second run is not idempotent: %+v\n%s", r.Summary, log)
	}

	// 3. A failing task on one host; the hosts to run again; the re-run on that host only.
	r, log = l.play(vars(`,"fail_on":"lw-cactest-2"`))
	if r.Failure == nil || r.Failure.Host != "lw-cactest-2" || len(r.FailedHosts()) != 1 {
		t.Fatalf("failure: %+v\n%s", r.Failure, log)
	}
	r, log = l.play(vars(`,"fail_on":"lw-cactest-2"`), "PARAM_LIMIT="+strings.Join(r.FailedHosts(), ","))
	if len(r.Hosts) != 1 || r.Hosts[0].Host != "lw-cactest-2" {
		t.Fatalf("the re-run limited to the failed host ran on %+v\n%s", r.Hosts, log)
	}

	// 4. Drift: change a file by hand, stop a service by hand; a check-mode run reports it and changes nothing.
	l.exec(l.hosts[0], "sh", "-c", "echo message=by-hand > /etc/lw-demo.conf")
	l.exec(l.hosts[1], "systemctl", "stop", "lw-demo.service")
	r, log = l.play("PARAM_CHECK=true", "PARAM_DIFF=true", vars(""))
	d, ok := r.Drift()
	if !ok || d.Drifted != 2 || !strings.Contains(d.Text, "2 hosts drifted") {
		t.Fatalf("drift: %+v\n%s", d, log)
	}
	if strings.Contains(l.exec(l.hosts[0], "cat", "/etc/lw-demo.conf"), "hello from launchway") || strings.Contains(l.exec(l.hosts[1], "systemctl", "is-active", "lw-demo.service"), "\nactive") {
		t.Fatal("a check-mode run changed a host")
	}
	// ...and applying fixes it.
	if r, _ = l.play(vars("")); r.Summary.Changed != 2 {
		t.Fatalf("apply after drift: %+v", r.Summary)
	}

	// 5. The canary: the secret the playbook was given is nowhere in the report or the run's output.
	for _, f := range []string{".launchway/ansible/report.json"} {
		b, _ := os.ReadFile(filepath.Join(l.ws, f))
		if bytes.Contains(b, []byte(canary)) {
			t.Fatalf("the canary is in %s", f)
		}
	}
	if strings.Contains(log, canary) {
		t.Fatal("the canary is in the tool's output")
	}
	// ...though the host does hold it, as the playbook meant.
	if !strings.Contains(l.exec(l.hosts[0], "cat", "/etc/lw-demo.token"), canary) {
		t.Fatal("the token was not written to the host")
	}
}

func (l *lab) exec(host string, args ...string) string {
	l.t.Helper()
	if err := guard([]string{host}); err != nil {
		l.t.Fatal(err)
	}
	cmd := exec.Command("podman", append([]string{"exec", host}, args...)...)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestLabRefusesRealHosts(t *testing.T) {
	if err := guard([]string{"lw-cactest-1", "prod-db-1"}); err == nil {
		t.Fatal("a host that is not a lab container was allowed")
	}
	if err := guard([]string{"localhost"}); err == nil {
		t.Fatal("localhost was allowed")
	}
}
