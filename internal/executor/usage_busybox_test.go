package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The loop that reads a container's cgroup (sampleScript) and the wrapper that runs a step under it
// (measureWrapper) run here inside docker.io/library/busybox:1.37 with podman, as the step user with the pod's private
// HOME (stepHome) and a read-only root, the way a restricted step pod runs them. Like the other container tests it runs
// only with LAUNCHWAY_TEST_CONTAINERS=1 and a podman (it pulls the image).
func busyboxPodman(t *testing.T) string {
	t.Helper()
	if os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run the busybox script tests")
	}
	podman, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("no podman")
	}
	return podman
}

func runBusybox(t *testing.T, podman string, extra []string, script string, args ...string) string {
	t.Helper()
	a := append([]string{"run", "--rm", "--user", "1000:1000", "--read-only", "--env", "HOME=" + stepHome}, extra...)
	a = append(a, busyboxImage, "sh", "-c", script, "sh")
	a = append(a, args...)
	out, err := exec.Command(podman, a...).CombinedOutput()
	if err != nil {
		t.Fatalf("podman %v: %v\n%s", a, err, out)
	}
	return string(out)
}

func TestBusyboxReadsTheCgroupWithTheStepUsersHome(t *testing.T) {
	podman := busyboxPodman(t)
	out := runBusybox(t, podman, []string{"--tmpfs", stepHomeVolume + ":rw,mode=1777"}, "mkdir -p $HOME && "+sampleScript)
	line := strings.TrimSpace(out)
	s, ok := parseSample(line, time.Now())
	if !ok || s.mem <= 0 || s.method == "" {
		t.Fatalf("one reading under busybox 1.37 is not a sample: %q", out)
	}
	if strings.Count(line, "\n") != 0 {
		t.Errorf("the single reading printed more than one line: %q", out)
	}
	// The loop form gives a reading every second until it is stopped.
	loop := runBusybox(t, podman, nil, "timeout 3 sh -c \"$1\" sh loop; true", sampleScript)
	n := 0
	for _, l := range strings.Split(strings.TrimSpace(loop), "\n") {
		if _, ok := parseSample(l, time.Now()); ok {
			n++
		}
	}
	if n < 2 {
		t.Errorf("the loop gave %d readings in three seconds under busybox: %q", n, loop)
	}
}

func TestBusyboxRunsAStepUnderTheWrapperAndKeepsItsExitCode(t *testing.T) {
	podman := busyboxPodman(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.sh"), []byte("sleep 2\necho done\nexit 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := []string{"run", "--rm", "--user", "1000:1000", "--env", "HOME=" + stepHome, "--userns", "keep-id", "-v", dir + ":/launchway:rw,z", busyboxImage, "sh", "-c", measureWrapper()}
	out, err := exec.Command(podman, a...).CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
		t.Fatalf("the step's exit code 7 was not passed on: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "done") {
		t.Errorf("the step's output is missing: %q", out)
	}
	var u Usage
	readUsageFile(dir, 2*time.Second, &u)
	if !u.Measured || u.MemoryPeak <= 0 {
		t.Errorf("the wrapper's readings were not turned into a measure: %+v", u)
	}
}
