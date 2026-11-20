package executor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A cache restores many thousands of files. Their names used to be one
// chmod command line inside one sh -c string, which the system refuses past
// about 128 KiB ("argument list too long"). The command is now constant, and
// the names go in by standard input.
func TestPlacedChmodDoesNotGrowWithTheFiles(t *testing.T) {
	var files []PlacedFile
	for i := 0; i < 20000; i++ {
		files = append(files, PlacedFile{Path: fmt.Sprintf("node_modules/package-%05d/lib/some/deeply/nested/directory/file-%05d.js", i, i)})
	}
	if len(placedChmodCommand) > 200 || strings.Contains(placedChmodCommand, "package-") {
		t.Fatalf("the command must not carry the names: %q", placedChmodCommand)
	}
	in := placedChmodInput(files)
	if n := strings.Count(in, "\x00"); n != len(files) {
		t.Fatalf("%d names for %d files", n, len(files))
	}
	if len(in) < 1<<20 {
		t.Fatalf("the test needs more names than one command line holds, not %d bytes", len(in))
	}
	if !strings.HasPrefix(in, "./node_modules/package-00000/") {
		t.Errorf("names start with ./ so that none is taken for an option: %.40q", in)
	}
}

// The command works with the real xargs on thousands of files, whose names
// would not fit one command line, and with names that look like options or
// hold spaces.
func TestPlacedChmodChangesEveryFile(t *testing.T) {
	if _, err := exec.LookPath("xargs"); err != nil {
		t.Skip("no xargs")
	}
	dir := t.TempDir()
	var files []PlacedFile
	add := func(p string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, PlacedFile{Path: p})
	}
	for i := 0; i < 6000; i++ {
		add(fmt.Sprintf("d%d/a-rather-long-directory-name-to-fill-the-line/another-long-directory-name-here/file-%05d.txt", i%50, i))
	}
	add("-rf")
	add("with space/and'quote.txt")
	cmd := exec.Command("sh", "-c", strings.Replace(placedChmodCommand, "cd /workspace", "cd "+shellQuote(dir), 1))
	cmd.Stdin = strings.NewReader(placedChmodInput(files))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, f := range files {
		info, err := os.Stat(filepath.Join(dir, f.Path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o666 != 0o666 {
			t.Fatalf("%s is %v, not writable by all", f.Path, info.Mode())
		}
	}
}

// copyOutMany reads many files with one tar stream. A directory on the way
// that is a link to somewhere outside the workspace must be refused, as it is
// for a single file, though the file itself is a regular file there.
func TestCopyOutManyRefusesALinkedDirectory(t *testing.T) {
	for _, tool := range []string{"tar", "readlink"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside := filepath.Join(base, "workspace"), filepath.Join(base, "outside")
	for _, p := range []string{filepath.Join(root, "src", "deep"), outside} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "top.txt"))
	write(filepath.Join(root, "src", "a.txt"))
	write(filepath.Join(root, "src", "deep", "b.txt"))
	write(filepath.Join(outside, "secret.txt"))
	run := func(names ...string) (string, error) {
		cmd := exec.Command("sh", "-c", copyOutManyScript(root))
		cmd.Stdin = strings.NewReader(copyOutManyInput(names))
		var out, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err := cmd.Run()
		return out.String() + stderr.String(), err
	}

	good := []string{"top.txt", "src/a.txt", "src/deep/b.txt"}
	out, err := run(good...)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, n := range good {
		if !strings.Contains(out, n) {
			t.Errorf("%s is not in the archive", n)
		}
	}

	// The step turns a directory into a link to a place outside.
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	out, err = run("top.txt", "linked/secret.txt")
	if err == nil || strings.Contains(out, "secret.txt") && strings.Contains(out, "data") {
		t.Fatalf("a file inside a linked directory was read: %v %q", err, out)
	}
	if !strings.Contains(out, "not inside the workspace: linked") {
		t.Errorf("the refusal does not say why: %q", out)
	}
	// One level deeper, under a link.
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(outside, "sub", "x.txt"))
	if _, err := run("linked/sub/x.txt"); err == nil {
		t.Error("a file two levels under a linked directory was read")
	}
}

func TestCopyOutManyInputListsEachDirectoryOnce(t *testing.T) {
	in := copyOutManyInput([]string{"a.txt", "d/x", "d/y", "d/e/z"})
	if in != "d\nd/e\n\na.txt\nd/x\nd/y\nd/e/z\n" {
		t.Errorf("%q", in)
	}
}
