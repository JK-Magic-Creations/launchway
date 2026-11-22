package executor

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A fake workspace: a directory the tests fill in, standing for /workspace in
// the run's pod. The commands the executor would run in the pod run on it,
// with the path rewritten, so that the real scripts (the listing and the tar
// of an explicit list of names) are what is tested.
type fakeWorkspace struct {
	t    *testing.T
	root string
	k    *Kubernetes
}

func newFakeWorkspace(t *testing.T) *fakeWorkspace {
	t.Helper()
	for _, tool := range []string{"sh", "find", "stat", "tar", "readlink", "du"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &fakeWorkspace{t: t, root: filepath.Join(root, "workspace")}
	if err := os.MkdirAll(w.root, 0o755); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName("run_ctx", "me"), Namespace: "launchway-steps"}}
	w.k = kubeWith(KubernetesOptions{}, pod)
	w.k.owner = "me"
	w.k.runIn = func(ctx context.Context, _, _ string, cmd []string, stdin io.Reader, stdout, stderr io.Writer) error {
		args := make([]string, len(cmd))
		for i, a := range cmd {
			args[i] = strings.ReplaceAll(a, "/workspace", w.root)
		}
		c := exec.CommandContext(ctx, args[0], args[1:]...)
		c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
		return c.Run()
	}
	return w
}

// put writes files; a name ending in / is a directory; "name -> target" is a link.
func (w *fakeWorkspace) put(spec ...string) {
	w.t.Helper()
	for _, s := range spec {
		name, target, link := strings.Cut(s, " -> ")
		p := filepath.Join(w.root, filepath.FromSlash(name))
		var err error
		switch {
		case link:
			if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				err = os.Symlink(strings.ReplaceAll(target, "OUTSIDE", filepath.Dir(w.root)), p)
			}
		case strings.HasSuffix(name, "/"):
			err = os.MkdirAll(p, 0o755)
		default:
			name, content, _ := strings.Cut(name, "=")
			p = filepath.Join(w.root, filepath.FromSlash(name))
			if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
				err = os.WriteFile(p, []byte(content), 0o644)
			}
		}
		if err != nil {
			w.t.Fatal(err)
		}
	}
}

// big writes a file of n bytes.
func (w *fakeWorkspace) big(name string, n int) {
	w.t.Helper()
	p := filepath.Join(w.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *fakeWorkspace) export(c ContextSpec, lim ExportLimits) (string, error) {
	w.t.Helper()
	dst := filepath.Join(w.t.TempDir(), "context")
	got, err := w.k.ExportContext(context.Background(), Spec{RunID: "run_ctx"}, c, dst, lim)
	if err == nil && !got {
		w.t.Fatal("the run's pod was not found")
	}
	return dst, err
}

// tree lists what is in dir, file by file: regular files by name, links as
// "name -> target".
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out = append(out, rel+" -> "+target)
		case d.Type().IsRegular():
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func same(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the context holds\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

var dot = ContextSpec{Dir: ".", Dockerfile: "Dockerfile"}

// An allow-list: * leaves everything out, ! brings back what the build reads,
// and a later pattern leaves out some of that again. What is left out never
// leaves the pod, and the Dockerfile and the ignore file always arrive.
func TestExportKeepsWhatAnAllowListKeeps(t *testing.T) {
	w := newFakeWorkspace(t)
	w.put("Dockerfile=FROM scratch", "go.mod=module x", "README.md=hi", "src/a.go=package a", "src/gen/g.go=package g",
		"src/deep/b.go=package b", "node_modules/x/index.js=x", ".cache/go-build/aa=zz", "other/file=1",
		".dockerignore=# what the build reads\n*\n!go.mod\n!src\nsrc/gen\n")
	dst, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 1000})
	if err != nil {
		t.Fatal(err)
	}
	same(t, tree(t, dst), []string{"Dockerfile", ".dockerignore", "go.mod", "src/a.go", "src/deep/b.go"})
}

// Patterns that match at any depth leave out a nested node_modules and keep
// the rest of the package; ** and ! work as the builder reads them.
func TestExportLeavesOutNestedNodeModules(t *testing.T) {
	w := newFakeWorkspace(t)
	w.put("Dockerfile=FROM scratch", "package.json=1", "web/index.js=1", "web/node_modules/lib/a.js=1",
		"packages/a/index.js=1", "packages/a/node_modules/dep/b.js=1", "packages/b/node_modules/dep/c.js=1", "packages/b/main.js=1",
		"node_modules/top/t.js=1", "docs/a.md=1", "docs/keep.md=1",
		".dockerignore=**/node_modules\ndocs/*\n!docs/keep.md\n")
	dst, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 1000})
	if err != nil {
		t.Fatal(err)
	}
	same(t, tree(t, dst), []string{"Dockerfile", ".dockerignore", "package.json", "web/index.js", "packages/a/index.js", "packages/b/main.js", "docs/keep.md"})
}

// <Dockerfile>.dockerignore, beside the Dockerfile, is the ignore file when
// there is one; .dockerignore is then not read, as with the builder.
func TestExportUsesTheDockerfileSpecificIgnoreFile(t *testing.T) {
	w := newFakeWorkspace(t)
	w.put("app/Dockerfile=FROM scratch", "app/a.txt=1", "app/b.txt=1", "app/c.txt=1",
		"app/Dockerfile.dockerignore=b.txt\n", "app/.dockerignore=a.txt\n")
	c := ContextSpec{Dir: "app", Dockerfile: "app/Dockerfile"}
	dst, err := w.export(c, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 1000})
	if err != nil {
		t.Fatal(err)
	}
	same(t, tree(t, dst), []string{"Dockerfile", "Dockerfile.dockerignore", ".dockerignore", "a.txt", "c.txt"})
	// Without it, .dockerignore.
	if err := os.Remove(filepath.Join(w.root, "app", "Dockerfile.dockerignore")); err != nil {
		t.Fatal(err)
	}
	dst, err = w.export(c, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 1000})
	if err != nil {
		t.Fatal(err)
	}
	same(t, tree(t, dst), []string{"Dockerfile", ".dockerignore", "b.txt", "c.txt"})
}

// A symbolic link is copied as a link and never followed: a link to a file or
// directory outside the context brings nothing of that place, and a context
// that is itself a link out of the workspace is refused.
func TestExportDoesNotFollowLinksOutOfTheContext(t *testing.T) {
	w := newFakeWorkspace(t)
	outside := filepath.Join(filepath.Dir(w.root), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.put("Dockerfile=FROM scratch", "ok.txt=1", "pointer -> OUTSIDE/outside/secret.txt", "dirlink -> OUTSIDE/outside", "etc -> /etc",
		"ctxlink -> OUTSIDE/outside")
	dst, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 1000})
	if err != nil {
		t.Fatal(err)
	}
	got := tree(t, dst)
	for _, n := range got {
		if strings.Contains(n, "secret") && !strings.Contains(n, "->") {
			t.Errorf("a file from outside the context was copied: %s", n)
		}
	}
	want := []string{"Dockerfile", "ok.txt", "pointer -> " + filepath.Join(outside, "secret.txt"), "dirlink -> " + outside, "etc -> /etc", "ctxlink -> " + outside}
	same(t, got, want)

	// The context itself is a link to a place outside the workspace.
	_, err = w.export(ContextSpec{Dir: "ctxlink", Dockerfile: "ctxlink/Dockerfile"}, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 1000})
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("a context that is a link out was copied: %v", err)
	}
}

// The limits apply to what is kept. Gigabytes' worth (here megabytes) of
// caches that the ignore file leaves out do not count, and are never read out.
func TestExportAppliesTheLimitAfterTheIgnoreFile(t *testing.T) {
	w := newFakeWorkspace(t)
	w.put("Dockerfile=FROM scratch", "src/a.go=package a", ".dockerignore=*\n!src\n!Dockerfile\n")
	w.big(".cache/go-build/blob", 6<<20)
	w.big("node_modules/pkg/big.bin", 5<<20)
	if _, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 100}); err != nil {
		t.Fatalf("what the ignore file leaves out counted against the limit: %v", err)
	}
	// Without the ignore file the same context is over the limit.
	if err := os.Remove(filepath.Join(w.root, ".dockerignore")); err != nil {
		t.Fatal(err)
	}
	_, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 100})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want too large, got %v", err)
	}
	if !strings.Contains(err.Error(), "there is no .dockerignore") {
		t.Errorf("the message does not say there is no ignore file: %v", err)
	}
}

// The message says how much was kept, how much the ignore file left out and
// the largest directories kept, so that a person can see what to ignore.
func TestExportTooLargeMessageSaysWhatToIgnore(t *testing.T) {
	w := newFakeWorkspace(t)
	w.put("Dockerfile=FROM scratch", ".dockerignore=node_modules\n*.log\n")
	w.big("assets/video/a.bin", 3<<20)
	w.big("assets/video/b.bin", 1<<20)
	w.big("data/set.bin", 2<<20)
	w.big("docs/x.md", 100<<10)
	w.big("src/main.go", 10<<10)
	w.big("node_modules/p/q.js", 4<<20)
	w.big("build.log", 1<<20)
	_, err := w.export(dot, ExportLimits{MaxBytes: 4 << 20, MaxFiles: 1000})
	var tl *ContextTooLarge
	if !errors.As(err, &tl) || !errors.Is(err, ErrTooLarge) {
		t.Fatalf("want a ContextTooLarge, got %v", err)
	}
	for _, want := range []string{"keeps 6.1 MiB", "after .dockerignore", "more than the 4 MiB that images.max_context_mib allows",
		"left out at least 5 MiB", "assets/video (4 MiB)", "data (2 MiB)", "docs (100 KiB)", "Add what the build does not read to .dockerignore"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in\n%v", want, err)
		}
	}
	if len(tl.Top) != 3 || tl.Top[0].Path != "assets/video" {
		t.Errorf("the largest directories are %+v", tl.Top)
	}
}

// The file count is checked on what is kept too.
func TestExportAppliesTheFileCountToWhatIsKept(t *testing.T) {
	w := newFakeWorkspace(t)
	w.put("Dockerfile=FROM scratch", ".dockerignore=junk\n")
	for _, n := range []string{"a", "b", "c", "d"} {
		w.put("src/" + n + "=1")
	}
	for _, n := range []string{"1", "2", "3", "4", "5", "6"} {
		w.put("junk/" + n + "=1")
	}
	if _, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 8}); err != nil {
		t.Fatalf("ignored files counted: %v", err)
	}
	_, err := w.export(dot, ExportLimits{MaxBytes: 1 << 20, MaxFiles: 5})
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "more than the 5 files allowed") {
		t.Fatalf("%v", err)
	}
}

// A run with no pod yet has nothing to copy out: the checkout is the workspace.
func TestExportWithoutAPodCopiesNothing(t *testing.T) {
	k := kubeWith(KubernetesOptions{})
	k.owner = "me"
	got, err := k.ExportContext(context.Background(), Spec{RunID: "run_none"}, dot, t.TempDir(), ExportLimits{})
	if got || err != nil {
		t.Fatalf("%v %v", got, err)
	}
}

// The context on the runner's own disk (the run's first step, or an executor
// with no pod) has the same rules and the same message.
func TestLocalContextHasTheSameRules(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", 10)
	write("src/a.go", 100)
	write(".cache/blob", 6<<20)
	write("node_modules/x/y.js", 5<<20)
	lim := ExportLimits{MaxBytes: 1 << 20, MaxFiles: 100}
	if err := CheckLocalContext(dir, dot, lim); !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "there is no .dockerignore") {
		t.Fatalf("without an ignore file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("*\n!src\n!Dockerfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckLocalContext(dir, dot, lim); err != nil {
		t.Fatalf("with the allow-list: %v", err)
	}
	// The Dockerfile-specific file wins, as for the builder.
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.dockerignore"), []byte("src\n.cache\nnode_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckLocalContext(dir, dot, lim); err != nil {
		t.Fatalf("with the Dockerfile's own: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.dockerignore"), []byte("src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckLocalContext(dir, dot, lim)
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "after Dockerfile.dockerignore") || !strings.Contains(err.Error(), ".cache (6 MiB)") {
		t.Fatalf("over the limit: %v", err)
	}
}

// The listing of the pod's files is read as records and anything odd is left out.
func TestParseContextList(t *testing.T) {
	got := parseContextList("directory|4096|./src\nregular file|12|./src/a.go\nregular empty file|0|./src/empty\nsymbolic link|7|./l\n" +
		"fifo|0|./pipe\nregular file|5|../escape\nregular file|x|./bad\nnot a record\nregular file|3|./with|bar\n")
	var names []string
	for _, e := range got {
		names = append(names, e.Path)
	}
	if strings.Join(names, ",") != "src,src/a.go,src/empty,l,with|bar" {
		t.Fatalf("%v", names)
	}
	if !got[0].Dir || got[1].Size != 12 || !got[3].Link {
		t.Fatalf("%+v", got)
	}
}
