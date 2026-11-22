package executor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMatchGlob(t *testing.T) {
	for _, c := range []struct {
		glob, name string
		want       bool
	}{
		{"reports/*.xml", "reports/unit.xml", true},
		{"reports/*.xml", "reports/sub/unit.xml", false},
		{"reports/*.xml", "reports/unit.json", false},
		{"**/TEST-*.xml", "TEST-a.xml", true},
		{"**/TEST-*.xml", "mod/target/surefire-reports/TEST-a.xml", true},
		{"**/TEST-*.xml", "node_modules/x/TEST-a.xml", false},
		{"**/TEST-*.xml", "a/.git/TEST-a.xml", false},
		{"node_modules/x/*.xml", "node_modules/x/r.xml", true},
		{"a/**/b/*.xml", "a/b/r.xml", true},
		{"a/**/b/*.xml", "a/x/y/b/r.xml", true},
		{"a/**/b/*.xml", "a/x/y/c/r.xml", false},
		{"go.json", "go.json", true},
		{"./reports/**/junit.xml", "reports/a/junit.xml", true}, // a template writes the working directory in front, and it may be "."
		{"backend/reports/**/junit.xml", "app/reports/junit.xml", false},
		{"report[12].xml", "report2.xml", true},
	} {
		if got := matchGlob(c.glob, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v", c.glob, c.name, got)
		}
	}
	for glob, want := range map[string]string{
		"reports/*.xml": "reports 1", "go.json": ". 1", "**/x.xml": ". -1", "a/b/**/c/*.xml": "a/b -1", "a/*/c.xml": "a 2",
	} {
		base, depth := globBase(glob)
		if got := fmt.Sprintf("%s %d", base, depth); got != want {
			t.Errorf("globBase(%q) = %s, want %s", glob, got, want)
		}
	}
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func paths(c *Collected) []string {
	var out []string
	for _, f := range c.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestCollectLocal(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	writeFiles(t, ws, map[string]string{
		"reports/a.xml": "aaaa", "reports/b.xml": "bbbbbbbbbb", "reports/deep/c.xml": "c",
		"mod/target/surefire-reports/TEST-x.xml": "x", "node_modules/pkg/TEST-n.xml": "n", "README.md": "readme",
	})
	writeFiles(t, outside, map[string]string{"secret.xml": "the runner's secret", "dir/s.xml": "s"})
	os.Symlink(filepath.Join(outside, "secret.xml"), filepath.Join(ws, "reports", "link.xml"))
	os.Symlink(filepath.Join(outside, "dir"), filepath.Join(ws, "reports", "linkdir"))
	os.Symlink(filepath.Join(outside, "dir"), filepath.Join(ws, "outdir"))
	if err := syscall.Mkfifo(filepath.Join(ws, "reports", "pipe.xml"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	got, err := collectLocal(ctx, ws, []string{"reports/*.xml", "**/TEST-*.xml", "missing/*.xml", "outdir/*.xml"}, CollectLimits{}.WithDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"mod/target/surefire-reports/TEST-x.xml", "reports/a.xml", "reports/b.xml"}; !slices.Equal(paths(got), want) {
		t.Fatalf("collected %v, want %v", paths(got), want)
	}
	notes := strings.Join(got.Notes, "\n")
	if !strings.Contains(notes, "missing/*.xml matched no files") || !strings.Contains(notes, "outdir/*.xml matched no files") {
		t.Fatalf("notes: %s", notes)
	}
	for _, f := range got.Files {
		if strings.Contains(string(f.Data), "secret") {
			t.Fatal("a file outside the workspace was read")
		}
	}

	// Limits: files, bytes per file, bytes in all.
	got, _ = collectLocal(ctx, ws, []string{"reports/**/*.xml"}, CollectLimits{MaxFiles: 2, MaxFileBytes: 6, MaxBytes: 8})
	if !slices.Equal(paths(got), []string{"reports/a.xml", "reports/b.xml"}) {
		t.Fatalf("limited to %v", paths(got))
	}
	if string(got.Files[0].Data) != "aaaa" || string(got.Files[1].Data) != "bbbb" || !got.Files[1].Cut || got.Files[1].Size != 10 {
		t.Fatalf("cut files: %+v", got.Files)
	}
	notes = strings.Join(got.Notes, "\n")
	if !strings.Contains(notes, "3 files matched; only the first 2") || !strings.Contains(notes, "reports/b.xml is 10 bytes; only its first 4 bytes") {
		t.Fatalf("notes: %s", notes)
	}

	// A workspace that is not there is an error, not a panic.
	if _, err := collectLocal(ctx, filepath.Join(ws, "nope"), []string{"*.xml"}, CollectLimits{}.WithDefaults()); err == nil {
		t.Fatal("a missing workspace was collected from")
	}
}

// collectAfterFailingStep runs a step that writes reports and fails, then
// collects them, as the runner does.
func collectAfterFailingStep(t *testing.T, ex Executor, s Spec) {
	t.Helper()
	var c capture
	code, err := ex.Run(context.Background(), s, c.sink)
	if err != nil || code != 3 {
		t.Fatalf("code %d err %v\n%s", code, err, c.text())
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	got, err := Collect(ctx, ex, s, []string{"reports/*.xml", "**/TEST-*.xml", "none/*.json"},
		CollectLimits{MaxFiles: 10, MaxFileBytes: 1000, MaxBytes: 5000})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if want := []string{"reports/big.xml", "reports/unit.xml", "target/r/TEST-a.xml"}; !slices.Equal(paths(got), want) {
		t.Fatalf("collected %v, want %v; notes %v", paths(got), want, got.Notes)
	}
	if string(got.Files[1].Data) != "<testsuite/>\n" {
		t.Fatalf("unit.xml: %q", got.Files[1].Data)
	}
	if !got.Files[0].Cut || len(got.Files[0].Data) != 1000 {
		t.Fatalf("big.xml: %d bytes, cut %v", len(got.Files[0].Data), got.Files[0].Cut)
	}
	notes := strings.Join(got.Notes, "\n")
	if !strings.Contains(notes, "none/*.json matched no files") || !strings.Contains(notes, "big.xml") {
		t.Fatalf("notes: %s", notes)
	}
	for _, f := range got.Files {
		if strings.Contains(string(f.Data), "root:") {
			t.Fatal("a symbolic link to a file outside the workspace was followed")
		}
	}
}

const reportScript = `mkdir -p reports target/r node_modules/m
echo '<testsuite/>' > reports/unit.xml
head -c 5000 /dev/zero | tr '\0' x > reports/big.xml
echo '<testsuite/>' > target/r/TEST-a.xml
echo '<testsuite/>' > node_modules/m/TEST-skip.xml
ln -s /etc/passwd reports/passwd.xml
mkfifo reports/fifo.xml
exit 3`

func TestContainerCollect(t *testing.T) {
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
	collectAfterFailingStep(t, c, spec(t, reportScript))
}

func TestKubernetesCollect(t *testing.T) {
	k := kube(t)
	ctx := context.Background()
	runID := fmt.Sprintf("run_kc%d", time.Now().UnixNano())
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(runID, k.owner), metav1.DeleteOptions{})
	s := kspec(t, runID, "collect", alpine, reportScript, t.TempDir())
	collectAfterFailingStep(t, k, s)

	// A step stopped by a timeout: what it wrote before is still there.
	tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var c capture
	s2 := kspec(t, runID, "slow", alpine, "mkdir -p late; echo '<testsuite/>' > late/r.xml; sleep 300", s.Workspace)
	if _, err := k.Run(tctx, s2, c.sink); err == nil {
		t.Fatal("the slow step was not stopped")
	}
	got, err := Collect(ctx, k, s2, []string{"late/*.xml"}, CollectLimits{})
	if err != nil || !slices.Equal(paths(got), []string{"late/r.xml"}) {
		t.Fatalf("after a timeout: %v %v %v", paths(got), got.Notes, err)
	}
}
