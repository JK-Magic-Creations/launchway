package executor

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The helper container of a run's pod is busybox, whose find, stat, du, readlink and tar are not GNU's. The scripts
// that list a build context and read out the files it keeps run here inside docker.io/library/busybox:1.37 with
// podman, on the same tree as under the host's own tools, and the two must give the same context. Like the other
// container tests it runs only with LAUNCHWAY_TEST_CONTAINERS=1 and a podman (it pulls the image).
const busyboxImage = "docker.io/library/busybox:1.37"

func newBusyboxWorkspace(t *testing.T) *fakeWorkspace {
	t.Helper()
	if os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run the busybox script tests")
	}
	podman, err := exec.LookPath("podman")
	if err != nil {
		t.Skip("no podman")
	}
	w := newFakeWorkspace(t)
	w.k.runIn = func(ctx context.Context, _, _ string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
		args := append([]string{"run", "--rm", "-i", "-v", w.root + ":/workspace:ro,z", busyboxImage}, cmd...)
		c := exec.CommandContext(ctx, podman, args...)
		c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
		return c.Run()
	}
	return w
}

// fillContext writes the tree both runs read: an allow-list ignore file, a nested node_modules, a link inside the
// workspace, a link out of it, and a name with a space.
func fillContext(w *fakeWorkspace) {
	w.put("Dockerfile=FROM scratch", ".dockerignore=*\n!Dockerfile\n!src/**\n!dir with space/**\n!link-in\n",
		"src/a.go=package a", "src/node_modules/x/b.js=b", "node_modules/y/c.js=c", "sub/keep.txt=k",
		"dir with space/my file.txt=e", "empty.txt=", "link-in -> src", "link-etc -> /etc")
	w.big(".cache/go-build/blob", 2<<20)
}

func TestBusyboxScriptsGiveTheSameContextAsTheHostsTools(t *testing.T) {
	gnu, bb := newFakeWorkspace(t), newBusyboxWorkspace(t)
	fillContext(gnu)
	fillContext(bb)
	lim := ExportLimits{MaxBytes: 1 << 20, MaxFiles: 100}
	gd, gerr := gnu.export(dot, lim)
	bd, berr := bb.export(dot, lim)
	if gerr != nil || berr != nil {
		t.Fatalf("host tools: %v; busybox: %v", gerr, berr)
	}
	got, want := tree(t, bd), tree(t, gd)
	same(t, got, want)
	for _, must := range []string{"Dockerfile", "src/a.go", "src/node_modules/x/b.js", "dir with space/my file.txt", "link-in -> src"} {
		found := false
		for _, g := range got {
			found = found || g == must
		}
		if !found {
			t.Errorf("busybox left %q out of the context: %v", must, got)
		}
	}
	for _, g := range got {
		if strings.HasPrefix(g, "node_modules") || strings.HasPrefix(g, ".cache") || strings.HasPrefix(g, "sub/") || strings.HasPrefix(g, "link-etc") {
			t.Errorf("busybox kept %q, which the allow-list leaves out", g)
		}
	}
}

// A context that is a link out of the workspace is refused by busybox's readlink -f as it is by the host's, and what
// the ignore file does not keep is counted out of the limit, with du's sizes in the message.
func TestBusyboxRefusesALinkOutAndSizesWhatItLeavesOut(t *testing.T) {
	w := newBusyboxWorkspace(t)
	fillContext(w)
	if _, err := w.export(ContextSpec{Dir: "link-etc", Dockerfile: "link-etc/Dockerfile"}, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 100}); err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("a context that leads out through a link: %v", err)
	}
	if err := os.Remove(filepath.Join(w.root, ".dockerignore")); err != nil {
		t.Fatal(err)
	}
	_, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 100})
	var tl *ContextTooLarge
	if !errors.As(err, &tl) || !strings.Contains(err.Error(), ".cache") {
		t.Fatalf("want a too-large context that names .cache (du under busybox): %v", err)
	}
}
