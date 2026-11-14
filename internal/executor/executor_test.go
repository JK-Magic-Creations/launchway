package executor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

type capture struct {
	mu    sync.Mutex
	lines []string
}

func (c *capture) sink(stream, text string) {
	c.mu.Lock()
	c.lines = append(c.lines, stream+": "+text)
	c.mu.Unlock()
}

func (c *capture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func spec(t *testing.T, script string) Spec {
	return Spec{
		Name:  "launchway-test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
		Image: "docker.io/library/alpine:3.22", Script: script,
		Env: map[string]string{"GREETING": "hello world"}, Workspace: t.TempDir(), MetaDir: t.TempDir(),
	}
}

func exercise(t *testing.T, ex Executor) {
	t.Run("output, env and outputs", func(t *testing.T) {
		var c capture
		s := spec(t, "echo \"$GREETING\"\necho oops >&2\necho digest=sha256:abc >> \"$LAUNCHWAY_OUTPUTS\"\ntouch made-here")
		code, err := ex.Run(context.Background(), s, c.sink)
		if err != nil || code != 0 {
			t.Fatalf("code %d err %v\n%s", code, err, c.text())
		}
		if !strings.Contains(c.text(), "stdout: hello world") || !strings.Contains(c.text(), "stderr: oops") {
			t.Fatalf("output:\n%s", c.text())
		}
		if got := ReadOutputs(s.MetaDir)["digest"]; got != "sha256:abc" {
			t.Fatalf("outputs: %q", got)
		}
		if _, err := os.Stat(s.Workspace + "/made-here"); err != nil {
			t.Fatalf("the step did not run in the workspace: %v", err)
		}
	})
	t.Run("failing command stops the script", func(t *testing.T) {
		var c capture
		code, err := ex.Run(context.Background(), spec(t, "false\necho should-not-print"), c.sink)
		if err != nil || code == 0 {
			t.Fatalf("code %d err %v", code, err)
		}
		if strings.Contains(c.text(), "should-not-print") {
			t.Fatal("the script carried on after a failure")
		}
	})
	t.Run("cancel stops it", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var c capture
		start := time.Now()
		_, err := ex.Run(ctx, spec(t, "echo started\nsleep 300"), c.sink)
		if err == nil {
			t.Fatal("expected the context error")
		}
		if time.Since(start) > 30*time.Second {
			t.Fatalf("took %s to stop", time.Since(start))
		}
	})
}

func TestHost(t *testing.T) { exercise(t, &Host{}) }

func TestContainer(t *testing.T) {
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
	exercise(t, c)
}

func TestReadOutputs(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/"+OutputsFile, []byte("a=1\n\nnot-a-pair\n b = 2 \nc=x=y\n"), 0o644)
	got := ReadOutputs(dir)
	if got["a"] != "1" || got["b"] != " 2 " || got["c"] != "x=y" || len(got) != 3 {
		t.Fatalf("%v", got)
	}
}

func TestLongLinesAreSplit(t *testing.T) {
	var c capture
	s := spec(t, "head -c 200000 /dev/zero | tr '\\0' 'x'; echo; echo done")
	if code, err := (&Host{}).Run(context.Background(), s, c.sink); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.lines) < 4 || c.lines[len(c.lines)-1] != "stdout: done" {
		t.Fatalf("got %d lines", len(c.lines))
	}
	for _, l := range c.lines {
		if len(l) > 64*1024+10 {
			t.Fatalf("a line of %d bytes was not split", len(l))
		}
	}
}

func TestMaskingSurvivesLineSplit(t *testing.T) {
	const secret = "SECRET-VALUE"
	// The secret straddles the 64 KiB point where the line is split.
	line := strings.Repeat("x", maxLine-5) + secret + strings.Repeat("y", 100)
	var c capture
	var wg sync.WaitGroup
	wg.Add(1)
	mask := func(s string) string { return strings.ReplaceAll(s, secret, "***") }
	pipeLines(strings.NewReader(line+"\nafter "+secret+"\r\n"), "stdout", mask, c.sink, &wg)
	wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	want := strings.Repeat("x", maxLine-5) + "***" + strings.Repeat("y", 100)
	if len(c.lines) != 3 {
		t.Fatalf("got %d lines, want the long line in two pieces and one more", len(c.lines))
	}
	if got := strings.TrimPrefix(c.lines[0], "stdout: ") + strings.TrimPrefix(c.lines[1], "stdout: "); got != want {
		t.Fatalf("the long line was not masked whole (%d bytes)", len(got))
	}
	if c.lines[2] != "stdout: after ***" {
		t.Fatalf("got %q", c.lines[2])
	}
}

func TestContainerRejectsRelativePaths(t *testing.T) {
	c := &Container{Bin: "/bin/false"}
	_, err := c.Run(context.Background(), Spec{Name: "x", Image: "i", Script: "true", Workspace: "relative/dir", MetaDir: t.TempDir()}, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("err %v", err)
	}
}

func TestPodmanService(t *testing.T) {
	if os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	bin, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("podman not installed")
	}
	c := &Container{Bin: bin, Owner: "test" + strings.ToLower(time.Now().Format("150405"))}
	runID := "run_ps" + time.Now().Format("150405.000")
	s := spec(t, reachService)
	s.RunID = runID
	s.Containers = []ContainerSpec{{Name: "alpine", Image: s.Image}, webService}
	s.Container = "alpine"
	var out capture
	code, err := c.Run(context.Background(), s, out.sink)
	if err != nil || code != 0 || !strings.Contains(out.text(), "service reachable") {
		t.Fatalf("code %d err %v\n%s", code, err, out.text())
	}
	// Cleanup removes the pod once the run is unknown or finished.
	if err := c.Cleanup(context.Background(), func(context.Context, []string) (map[string]RunInfo, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if exec.Command(bin, "pod", "exists", c.podName(runID)).Run() == nil {
		t.Fatal("pod left behind")
	}
}
