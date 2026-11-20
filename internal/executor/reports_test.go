package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// reportsScript writes what a test step with reports writes: a JUnit
// report, an attachments directory with a screenshot, and links that try to
// lead out of it: a link to a file outside, a link to a directory outside,
// and a directory outside the workspace reached through a link.
const reportsScript = `mkdir -p reports test-results/checkout
cat > reports/junit.xml <<'EOF'
<testsuite name="shop"><testcase classname="shop" name="checkout"><failure message="boom">trace</failure>
<system-out>[[ATTACHMENT|test-results/checkout/shot.png]]</system-out></testcase></testsuite>
EOF
printf '\211PNG\r\n\032\n' > test-results/checkout/shot.png
ln -s /etc/passwd test-results/checkout/passwd.png
ln -s /etc test-results/etc
ln -s /etc esc
echo done`

// reportsAndAttachments runs reportsScript and collects what a runner
// collects after such a step: the report files, and the attachments
// directory kept as an artifact. Nothing outside the workspace is read.
func reportsAndAttachments(t *testing.T, ex Executor, s Spec) {
	t.Helper()
	ctx := context.Background()
	var c capture
	if code, err := ex.Run(ctx, s, c.sink); err != nil || code != 0 {
		t.Fatalf("step: %d %v\n%s", code, err, c.text())
	}
	got, err := Collect(ctx, ex, s, []string{"reports/*.xml"}, CollectLimits{})
	if err != nil || len(got.Files) != 1 || !strings.Contains(string(got.Files[0].Data), "[[ATTACHMENT|test-results/checkout/shot.png]]") {
		t.Fatalf("collected %+v %v", got, err)
	}
	staged, err := Stage(ctx, ex, s, []string{"test-results"}, StageLimits{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Files) != 1 || staged.Files[0].Path != "test-results/checkout/shot.png" || staged.Files[0].Size != 8 {
		t.Fatalf("staged %+v (notes %v)", staged.Files, staged.Notes)
	}
	notes := strings.Join(staged.Notes, "\n")
	for _, want := range []string{"test-results/checkout/passwd.png is not a regular file", "test-results/etc is not a regular file"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes %q lack %q", notes, want)
		}
	}
	// A path through a link to a directory outside the workspace is never
	// read, even when the file at its end is a regular file.
	if s, err := Stage(ctx, ex, s, []string{"esc/passwd"}, StageLimits{}, t.TempDir()); !errors.Is(err, ErrNotKept) {
		t.Fatalf("esc/passwd: %+v %v", s, err)
	}
	if c, err := Collect(ctx, ex, s, []string{"esc/passwd", "test-results/etc/passwd"}, CollectLimits{}); err != nil || len(c.Files) != 0 {
		t.Fatalf("collected through a link: %+v %v", c, err)
	}
}

func TestReportsAndAttachmentsOnTheHost(t *testing.T) {
	ws := t.TempDir()
	reportsAndAttachments(t, &Host{}, Spec{Name: "e2e", Script: reportsScript, Workspace: ws, MetaDir: t.TempDir()})
	if _, err := os.Lstat(filepath.Join(ws, "esc")); err != nil {
		t.Fatal(err)
	}
}

// The same from a container: the links point at the container's /etc,
// which the runner must not read either.
func TestContainerReportsAndAttachments(t *testing.T) {
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
	reportsAndAttachments(t, c, spec(t, reportsScript))
}

// The same from a restricted step pod.
func TestKubernetesReportsAndAttachments(t *testing.T) {
	k := kube(t)
	requireRestrictedNamespace(t, k)
	runID := fmt.Sprintf("run_kr%d", time.Now().UnixNano())
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(context.Background(), podName(runID, k.owner), metav1.DeleteOptions{})
	reportsAndAttachments(t, k, kspec(t, runID, "e2e", alpine, reportsScript, t.TempDir()))
}
