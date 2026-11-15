package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, name, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func shaOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestStageLocal(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "bin/app", "binary", 0o755)
	write(t, ws, "dist/a.tar.gz", "a", 0o644)
	write(t, ws, "dist/sub/b.tar.gz", "b", 0o644)
	write(t, ws, "dist/readme", "r", 0o644)
	write(t, ws, "site/index.html", "<p>", 0o644)
	write(t, ws, "site/css/x.css", "x", 0o644)
	write(t, ws, "site/node_modules/m.js", "m", 0o644)
	outside := t.TempDir()
	write(t, outside, "secret", "do not read", 0o644)
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(ws, "bin/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(ws, "site/escape")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	got, err := Stage(context.Background(), &Host{}, Spec{Workspace: ws}, []string{"bin/*", "dist/*.tar.gz", "site", "nothing/*"}, StageLimits{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range got.Files {
		paths = append(paths, f.Path)
		data, _ := os.ReadFile(f.Local)
		if f.SHA256 != shaOf(string(data)) || f.Size != int64(len(data)) {
			t.Errorf("%s: hash or size wrong", f.Path)
		}
	}
	// A glob that names a directory keeps what is below it, except
	// node_modules; symbolic links are not followed or kept.
	if want := "bin/app dist/a.tar.gz site/css/x.css site/index.html"; strings.Join(paths, " ") != want {
		t.Fatalf("staged %v, want %s", paths, want)
	}
	if got.Files[0].Mode != 0o755 || got.Files[1].Mode != 0o644 {
		t.Fatalf("modes %o %o", got.Files[0].Mode, got.Files[1].Mode)
	}
	notes := strings.Join(got.Notes, "\n")
	if !strings.Contains(notes, "nothing/* matched no files") || !strings.Contains(notes, "bin/link is not a regular file") {
		t.Fatalf("notes %q", notes)
	}
	for _, f := range got.Files {
		if data, _ := os.ReadFile(f.Local); strings.Contains(string(data), "do not read") {
			t.Fatal("read through a symbolic link")
		}
	}
}

func TestStageLimits(t *testing.T) {
	ws := t.TempDir()
	write(t, ws, "a", strings.Repeat("x", 100), 0o644)
	write(t, ws, "b", strings.Repeat("y", 100), 0o644)
	for _, tc := range []struct {
		name  string
		globs []string
		lim   StageLimits
		want  string
	}{
		{"nothing", []string{"none/*"}, StageLimits{}, "matched no files"},
		{"a file too large", []string{"a"}, StageLimits{MaxFileBytes: 99}, "larger than 99 bytes"},
		{"too much together", []string{"*"}, StageLimits{MaxBytes: 150}, "more than 150 bytes"},
		{"too many files", []string{"*"}, StageLimits{MaxFiles: 1}, "at most 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Stage(context.Background(), &Host{}, Spec{Workspace: ws}, tc.globs, tc.lim, t.TempDir())
			if !errors.Is(err, ErrNotKept) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPlaceLocal(t *testing.T) {
	ws := t.TempDir()
	src := t.TempDir()
	write(t, src, "1", "new app", 0o600)
	write(t, src, "2", "lib", 0o600)
	write(t, ws, "out/bin/app", "old app", 0o644)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(ws, "evil")); err != nil {
		t.Fatal(err)
	}
	files := []PlacedFile{
		{Path: "out/bin/app", Local: filepath.Join(src, "1"), Mode: 0o755},
		{Path: "out/lib/x.so", Local: filepath.Join(src, "2"), Mode: 0o644},
	}
	if err := Place(context.Background(), &Host{}, Spec{Workspace: ws}, files, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "out/bin/app")); string(b) != "new app" {
		t.Fatalf("app is %q", b)
	}
	if info, _ := os.Stat(filepath.Join(ws, "out/bin/app")); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", info.Mode())
	}
	// A symbolic link an earlier step left cannot send a file outside.
	err := Place(context.Background(), &Host{}, Spec{Workspace: ws}, []PlacedFile{{Path: "evil/x", Local: filepath.Join(src, "1")}}, func(string, string) {})
	if err == nil {
		t.Fatal("placed a file through a symbolic link that leaves the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "x")); err == nil {
		t.Fatal("a file was written outside the workspace")
	}
	for _, bad := range []string{"../x", "/etc/x", "a/../../x", ""} {
		if err := Place(context.Background(), &Host{}, Spec{Workspace: ws}, []PlacedFile{{Path: bad, Local: filepath.Join(src, "1")}}, func(string, string) {}); err == nil {
			t.Errorf("placed %q", bad)
		}
	}
}

const artifactScript = `mkdir -p dist/sub build
printf 'built app' > dist/app
chmod +x dist/app
printf 'notes' > dist/sub/notes.txt
head -c 3000000 /dev/zero | tr '\0' z > dist/big.bin
ln -s /etc/passwd dist/passwd
echo kept-elsewhere > build/other`

// artifactsAcrossSteps keeps files a step wrote, puts them back into the
// workspace under another directory, and has a second step read them.
func artifactsAcrossSteps(t *testing.T, ex Executor, s1, s2 Spec) {
	t.Helper()
	ctx := context.Background()
	var c capture
	if code, err := ex.Run(ctx, s1, c.sink); err != nil || code != 0 {
		t.Fatalf("step 1: %d %v\n%s", code, err, c.text())
	}
	staged, err := Stage(ctx, ex, s1, []string{"dist"}, StageLimits{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range staged.Files {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, " ") != "dist/app dist/big.bin dist/sub/notes.txt" {
		t.Fatalf("staged %v (notes %v)", paths, staged.Notes)
	}
	if staged.Files[0].Mode != 0o755 || staged.Files[2].Mode != 0o644 || staged.Files[1].Size != 3000000 || staged.Files[0].SHA256 != shaOf("built app") {
		t.Fatalf("staged %+v", staged.Files)
	}
	if !strings.Contains(strings.Join(staged.Notes, "\n"), "dist/passwd is not a regular file") {
		t.Fatalf("notes %v", staged.Notes)
	}
	var placed []PlacedFile
	for _, f := range staged.Files {
		placed = append(placed, PlacedFile{Path: "fetched/" + f.Path, Local: f.Local, Mode: f.Mode})
	}
	if err := Place(ctx, ex, s2, placed, c.sink); err != nil {
		t.Fatalf("place: %v\n%s", err, c.text())
	}
	var c2 capture
	code, err := ex.Run(ctx, s2, c2.sink)
	if err != nil || code != 0 {
		t.Fatalf("step 2: %d %v\n%s", code, err, c2.text())
	}
	out := c2.text()
	for _, want := range []string{"stdout: built app", "stdout: notes", "stdout: 3000000", "stdout: runs"} {
		if !strings.Contains(out, want) {
			t.Fatalf("step 2 did not see %q:\n%s", want, out)
		}
	}
}

const readFetched = `cat fetched/dist/app; echo
cat fetched/dist/sub/notes.txt; echo
wc -c < fetched/dist/big.bin
[ -x fetched/dist/app ] && echo runs`

func TestContainerArtifacts(t *testing.T) {
	if testing.Short() || os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	c, err := DetectContainer()
	if err != nil {
		t.Skip(err)
	}
	s1 := spec(t, artifactScript)
	// The second step runs elsewhere: in a workspace of its own.
	s2 := spec(t, readFetched)
	s2.Name += "-2"
	artifactsAcrossSteps(t, c, s1, s2)
}
