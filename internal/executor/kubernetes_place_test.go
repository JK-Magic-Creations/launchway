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
