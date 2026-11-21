package executor

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// member is one entry of a hand-made archive.
type member struct {
	name     string
	typeflag byte
	data     []byte
	link     string
	mode     int64
	size     int64 // when not the data's length
}

// craft makes a zstd-compressed tar archive from members, as an attacker
// who can write the store would.
func craft(t *testing.T, members ...member) string {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, m := range members {
		tf := m.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		size := int64(len(m.data))
		if m.size != 0 {
			size = m.size
		}
		h := &tar.Header{Typeflag: tf, Name: m.name, Linkname: m.link, Mode: m.mode, Size: size, Format: tar.FormatPAX}
		if tf != tar.TypeReg {
			h.Size = 0
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if tf == tar.TypeReg {
			if _, err := tw.Write(m.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return compress(t, raw.Bytes())
}

func compress(t *testing.T, raw []byte) string {
	t.Helper()
	var out bytes.Buffer
	enc, err := zstd.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	enc.Write(raw)
	enc.Close()
	name := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(name, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func staged(t *testing.T, files map[string]string) []StagedFile {
	t.Helper()
	dir := t.TempDir()
	var out []StagedFile
	i := 0
	for name, data := range files {
		local := filepath.Join(dir, string(rune('a'+i)))
		i++
		if err := os.WriteFile(local, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		mode := uint32(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		out = append(out, StagedFile{Path: name, Local: local, Size: int64(len(data)), SHA256: sumOf(data), Mode: mode})
	}
	return out
}

func ck(in CacheKeyInput) string { k, _ := CacheKey(in); return k }

func sumOf(s string) string { return ck(CacheKeyInput{Platform: "p", Name: "n", Text: s}) } // any stable hex string

// The key depends on everything it is made of, and on nothing else.
func TestCacheKey(t *testing.T) {
	f := func(path, sum string) StagedFile { return StagedFile{Path: path, SHA256: sum} }
	in := func(mod func(*CacheKeyInput)) string {
		k := CacheKeyInput{Platform: "linux/amd64", Name: "pip", Text: "v1", Image: ImageIdentity{Ref: "python:3.13", ID: "sha256:aa"},
			Versions: []VersionValue{{Label: "command: python -V", Value: "Python 3.13.1"}}, Files: []StagedFile{f("a.txt", "11"), f("b.txt", "22")}}
		if mod != nil {
			mod(&k)
		}
		return ck(k)
	}
	base := in(nil)
	if len(base) != 64 {
		t.Fatalf("%q", base)
	}
	if again := in(func(k *CacheKeyInput) { k.Files = []StagedFile{f("b.txt", "22"), f("a.txt", "11")} }); again != base {
		t.Error("the order the files were found in changed the key")
	}
	for name, got := range map[string]string{
		"platform":                       in(func(k *CacheKeyInput) { k.Platform = "linux/arm64" }),
		"name":                           in(func(k *CacheKeyInput) { k.Name = "npm" }),
		"text":                           in(func(k *CacheKeyInput) { k.Text = "v2" }),
		"image digest (same tag)":        in(func(k *CacheKeyInput) { k.Image.ID = "sha256:bb" }),
		"image reference":                in(func(k *CacheKeyInput) { k.Image.Ref = "python:3.12" }),
		"a tool version output":          in(func(k *CacheKeyInput) { k.Versions[0].Value = "Python 3.13.2" }),
		"a version input removed":        in(func(k *CacheKeyInput) { k.Versions = nil }),
		"a file changed":                 in(func(k *CacheKeyInput) { k.Files[0].SHA256 = "12" }),
		"a file renamed":                 in(func(k *CacheKeyInput) { k.Files[1].Path = "c.txt" }),
		"a file added":                   in(func(k *CacheKeyInput) { k.Files = append(k.Files, f("c.txt", "33")) }),
		"contents swapped between files": in(func(k *CacheKeyInput) { k.Files = []StagedFile{f("a.txt", "22"), f("b.txt", "11")} }),
		// Boundaries are not ambiguous: moving text into the name does not
		// make the same key.
		"text moved into the name": in(func(k *CacheKeyInput) { k.Name, k.Text = "pipv1", "" }),
	} {
		if got == base {
			t.Errorf("%s did not change the key", name)
		}
	}
}

// The parts say, in words, what the key is made of; the image is shown with
// what it resolved to.
func TestCacheKeyPartsNameTheImageAndVersions(t *testing.T) {
	_, parts := CacheKey(CacheKeyInput{Platform: "linux/amd64", Name: "m2", Image: ImageIdentity{Ref: "maven:3", ID: "sha256:abc"},
		Versions: []VersionValue{{Label: "command: java -version", Value: "openjdk 21"}}})
	text := ""
	for _, p := range parts {
		text += p.Label + "=" + p.Value + "\n"
	}
	for _, want := range []string{"platform=linux/amd64", "image=maven:3 = sha256:abc", "version command: java -version=openjdk 21"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
}

// A reference with a digest is that digest, and the host executor, which
// ignores the image, has none.
func TestResolveImageWithoutAnEngine(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	if id := ResolveImage(context.Background(), &Host{}, "golang@"+d); id.ID != d {
		t.Errorf("%+v", id)
	}
	if id := ResolveImage(context.Background(), &Host{}, "golang:1.26"); id.ID != "" || id.Ref != "golang:1.26" {
		t.Errorf("%+v", id)
	}
}

// What is packed comes back the same, with permissions reduced to 0644 and
// 0755, and the archive is the same bytes each time.
func TestPackAndUnpackRoundTrip(t *testing.T) {
	files := staged(t, map[string]string{".cache/pip/a/one.whl": strings.Repeat("one ", 1000), ".cache/pip/run.sh": "#!/bin/sh\n", ".cache/pip/empty": ""})
	dir := t.TempDir()
	a1, err := PackCache(files, filepath.Join(dir, "a1"))
	if err != nil {
		t.Fatal(err)
	}
	if a1.Files != 3 || a1.Bytes <= 0 || len(a1.SHA256) != 64 || a1.Raw != int64(4000+10) {
		t.Fatalf("%+v", a1)
	}
	un, err := UnpackCache(context.Background(), a1.Path, filepath.Join(dir, "out"), []string{".cache/pip"}, UnpackLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(un.Files) != 3 || un.Skipped != 0 || un.Bytes != 4010 {
		t.Fatalf("%+v", un)
	}
	got := map[string]PlacedFile{}
	for _, f := range un.Files {
		got[f.Path] = f
	}
	for _, want := range files {
		p, ok := got[want.Path]
		if !ok {
			t.Fatalf("%s is missing: %+v", want.Path, un.Files)
		}
		b, err := os.ReadFile(p.Local)
		w, _ := os.ReadFile(want.Local)
		if err != nil || !bytes.Equal(b, w) {
			t.Errorf("%s: %q %v", want.Path, b, err)
		}
		if p.Mode != want.Mode {
			t.Errorf("%s: mode %o, want %o", want.Path, p.Mode, want.Mode)
		}
		if !strings.HasPrefix(p.Local, filepath.Join(dir, "out")) {
			t.Errorf("%s was written outside the directory it was given: %s", want.Path, p.Local)
		}
	}
}

// Only members the step's own paths cover are written; the rest are left
// out, and a glob that no longer matches does not fail the restore.
func TestUnpackLeavesOutWhatThePathsDoNotCover(t *testing.T) {
	arc := craft(t,
		member{name: ".cache/pip/a", data: []byte("a")},
		member{name: "src/main.go", data: []byte("package main")},
		member{name: ".git/hooks/pre-commit", data: []byte("#!/bin/sh\nevil"), mode: 0o755},
		member{name: "Makefile", data: []byte("all:")},
	)
	un, err := UnpackCache(context.Background(), arc, t.TempDir(), []string{".cache/pip"}, UnpackLimits{})
	if err != nil || len(un.Files) != 1 || un.Files[0].Path != ".cache/pip/a" || un.Skipped != 3 {
		t.Fatalf("%+v %v", un, err)
	}
	// With a glob and a directory name, both are honoured as for artifacts.
	un, err = UnpackCache(context.Background(), arc, t.TempDir(), []string{"src/*.go", ".git"}, UnpackLimits{})
	if err != nil || len(un.Files) != 2 {
		t.Fatalf("%+v %v", un, err)
	}
}

// A hostile archive never writes anything outside the directory it is
// unpacked in, whatever its members say, and says so.
func TestHostileArchivesAreRefused(t *testing.T) {
	big := bytes.Repeat([]byte{0}, 1<<20)
	for name, tc := range map[string]struct {
		members []member
		lim     UnpackLimits
		want    string
	}{
		"parent directory":        {[]member{{name: "../outside", data: []byte("x")}}, UnpackLimits{}, "not a clean path"},
		"deep parent directory":   {[]member{{name: ".cache/../../outside", data: []byte("x")}}, UnpackLimits{}, "not a clean path"},
		"absolute":                {[]member{{name: "/etc/cron.d/x", data: []byte("x")}}, UnpackLimits{}, "absolute"},
		"dot":                     {[]member{{name: "./x", data: []byte("x")}}, UnpackLimits{}, "not a clean path"},
		"doubled slash":           {[]member{{name: ".cache//x", data: []byte("x")}}, UnpackLimits{}, "not a clean path"},
		"backslash":               {[]member{{name: `.cache\..\x`, data: []byte("x")}}, UnpackLimits{}, "not text"},
		"control character":       {[]member{{name: ".cache/x\x01", data: []byte("x")}}, UnpackLimits{}, "not text"},
		"new line":                {[]member{{name: ".cache/x\ny", data: []byte("x")}}, UnpackLimits{}, "not text"},
		"a name longer than 1024": {[]member{{name: ".cache/" + strings.Repeat("a", 1100), data: []byte("x")}}, UnpackLimits{}, "longer than 1024"},
		"symbolic link out":       {[]member{{name: ".cache/l", typeflag: tar.TypeSymlink, link: "/etc"}}, UnpackLimits{}, "not a regular file"},
		"symbolic link up":        {[]member{{name: ".cache/l", typeflag: tar.TypeSymlink, link: "../../.."}}, UnpackLimits{}, "not a regular file"},
		"hard link":               {[]member{{name: ".cache/h", typeflag: tar.TypeLink, link: "/etc/passwd"}}, UnpackLimits{}, "not a regular file"},
		"device":                  {[]member{{name: ".cache/dev", typeflag: tar.TypeChar}}, UnpackLimits{}, "not a regular file"},
		"fifo":                    {[]member{{name: ".cache/fifo", typeflag: tar.TypeFifo}}, UnpackLimits{}, "not a regular file"},
		"a file then a link of the same name": {[]member{{name: ".cache/a", data: []byte("x")},
			{name: ".cache/a", typeflag: tar.TypeSymlink, link: "/etc/passwd"}}, UnpackLimits{}, "not a regular file"},
		"listed twice":            {[]member{{name: ".cache/a", data: []byte("1")}, {name: ".cache/a", data: []byte("2")}}, UnpackLimits{}, "listed twice"},
		"one file over the limit": {[]member{{name: ".cache/a", data: big}}, UnpackLimits{MaxFileBytes: 1 << 19}, "more than the"},
		"too many files":          {[]member{{name: ".cache/a", data: []byte("1")}, {name: ".cache/b", data: []byte("1")}, {name: ".cache/c", data: []byte("1")}}, UnpackLimits{MaxFiles: 2}, "more than 2 files"},
		"too much together":       {[]member{{name: ".cache/a", data: big}, {name: ".cache/b", data: big}}, UnpackLimits{MaxTotalBytes: 3 << 19}, "expands to more than"},
	} {
		arc := craft(t, tc.members...)
		dir := t.TempDir()
		un, err := UnpackCache(context.Background(), arc, filepath.Join(dir, "out"), []string{".cache", "../outside", "/etc"}, tc.lim)
		if err == nil {
			t.Errorf("%s: accepted: %+v", name, un)
			continue
		}
		if !errors.Is(err, ErrCacheArchive) {
			t.Errorf("%s: %v is not an ErrCacheArchive", name, err)
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v does not say %q", name, err, tc.want)
		}
		// Nothing but numbered files, if anything, is under the directory,
		// and nothing is beside it.
		entries, _ := os.ReadDir(dir)
		if len(entries) > 1 {
			t.Errorf("%s: %d entries beside the directory unpacked into", name, len(entries))
		}
		filepath.Walk(filepath.Join(dir, "out"), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && !isNumber(filepath.Base(p)) {
				t.Errorf("%s: a file named %s was written; names in an archive are never used for files", name, p)
			}
			return nil
		})
	}
}

func isNumber(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// An archive that expands to far more than it weighs is refused before it
// fills the disk, and one that is not an archive at all, or is cut short,
// is refused too.
func TestExpansionBombsAndDamagedArchives(t *testing.T) {
	// 600 MiB of zeros weigh a few kilobytes: over the ratio, past its floor.
	zeros := make([]byte, 600<<20)
	arc := craft(t, member{name: ".cache/zeros", data: zeros})
	if st, _ := os.Stat(arc); st.Size() > 1<<20 {
		t.Fatalf("the bomb is %d bytes: not the bomb this test means", st.Size())
	}
	_, err := UnpackCache(context.Background(), arc, t.TempDir(), []string{".cache"}, UnpackLimits{MaxTotalBytes: 4 << 30})
	if !errors.Is(err, ErrCacheArchive) || !strings.Contains(err.Error(), "times its size") {
		t.Fatalf("%v", err)
	}
	// The total cap holds even with a generous ratio.
	_, err = UnpackCache(context.Background(), arc, t.TempDir(), []string{".cache"}, UnpackLimits{MaxTotalBytes: 100 << 20, MaxRatio: 1 << 30})
	if !errors.Is(err, ErrCacheArchive) || !strings.Contains(err.Error(), "expands to more than") {
		t.Fatalf("%v", err)
	}
	// Not an archive.
	junk := filepath.Join(t.TempDir(), "junk")
	os.WriteFile(junk, []byte("this is not a cache entry"), 0o600)
	if _, err := UnpackCache(context.Background(), junk, t.TempDir(), []string{".cache"}, UnpackLimits{}); !errors.Is(err, ErrCacheArchive) {
		t.Fatalf("%v", err)
	}
	// A gzip file, which is what an older kind of entry might be.
	gz := filepath.Join(t.TempDir(), "gz")
	os.WriteFile(gz, []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3, 0x2b, 0x49, 0x2d, 0x2e, 1, 0, 0xcc, 0x60, 0xd1, 0x3f, 4, 0, 0, 0}, 0o600)
	if _, err := UnpackCache(context.Background(), gz, t.TempDir(), []string{".cache"}, UnpackLimits{}); !errors.Is(err, ErrCacheArchive) {
		t.Fatalf("%v", err)
	}
	// Cut short.
	good := staged(t, map[string]string{".cache/a": strings.Repeat("abc", 100000)})
	a, err := PackCache(good, filepath.Join(t.TempDir(), "good"))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(a.Path)
	cut := filepath.Join(t.TempDir(), "cut")
	os.WriteFile(cut, data[:len(data)/2], 0o600)
	if _, err := UnpackCache(context.Background(), cut, t.TempDir(), []string{".cache"}, UnpackLimits{}); !errors.Is(err, ErrCacheArchive) {
		t.Fatalf("a cut archive: %v", err)
	}
	// The context ends the work.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := UnpackCache(ctx, a.Path, t.TempDir(), []string{".cache"}, UnpackLimits{}); err == nil {
		t.Fatal("a canceled restore went on")
	}
}

// Packing refuses what is not a path inside the workspace, and notices a
// file that changed after it was staged.
func TestPackRefusesBadStagedFiles(t *testing.T) {
	f := staged(t, map[string]string{"a": "abc"})
	f[0].Path = "../a"
	if _, err := PackCache(f, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("packed a path outside the workspace")
	}
	f = staged(t, map[string]string{"a": "abc"})
	f[0].Size = 10
	if _, err := PackCache(f, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("packed a file shorter than it was staged")
	}
}

// cacheScript writes what a dependency install leaves: many small files in
// nested directories, a big one, a program, and a link that must not be kept.
const cacheScript = `mkdir -p .cache/deps/sub/deeper
i=0; while [ $i -lt 300 ]; do echo "file $i" > .cache/deps/sub/f$i.txt; i=$((i+1)); done
echo deep > .cache/deps/sub/deeper/d.txt
head -c 3000000 /dev/zero | tr '\0' 'x' > .cache/deps/big.bin
printf '#!/bin/sh\necho hi\n' > .cache/deps/run.sh; chmod +x .cache/deps/run.sh
ln -s /etc/passwd .cache/deps/passwd
echo "pinned" > $(pwd)/requirements.txt`

const cacheCheck = `ls .cache/deps/sub | wc -l
wc -c < .cache/deps/big.bin
cat .cache/deps/sub/f299.txt .cache/deps/sub/deeper/d.txt
[ -x .cache/deps/run.sh ] && echo runs
[ -e .cache/deps/passwd ] && echo LINK-PRESENT || echo no-link`

// cacheAcrossSteps does what a runner does with a cache: works out the key
// from a file before the step, packs the step's directory after it, and
// restores the archive into another run's workspace, where a second step
// finds it.
func cacheAcrossSteps(t *testing.T, ex Executor, s1, s2 Spec) {
	t.Helper()
	ctx := context.Background()
	var c capture
	// Before the step: the key's file, from the runner's own checkout (a
	// pod does not exist yet on Kubernetes).
	if err := os.WriteFile(filepath.Join(s1.Workspace, "requirements.txt"), []byte("pinned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	keyFiles, err := StageWorkspace(ctx, ex, s1, []string{"requirements.txt"}, StageLimits{}, t.TempDir())
	if err != nil || len(keyFiles.Files) != 1 {
		t.Fatalf("%+v %v", keyFiles, err)
	}
	key := ck(CacheKeyInput{Platform: "test/arch", Name: "deps", Text: "v1", Files: keyFiles.Files})
	if code, err := ex.Run(ctx, s1, c.sink); err != nil || code != 0 {
		t.Fatalf("step 1: %d %v\n%s", code, err, c.text())
	}
	// After it: the same key from the same file as the step left it.
	after, err := StageWorkspace(ctx, ex, s1, []string{"requirements.txt"}, StageLimits{}, t.TempDir())
	if err != nil || ck(CacheKeyInput{Platform: "test/arch", Name: "deps", Text: "v1", Files: after.Files}) != key {
		t.Fatalf("the key changed: %+v %v", after, err)
	}
	staged, err := StageWorkspace(ctx, ex, s1, []string{".cache/deps"}, StageLimits{MaxFiles: 100000}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(staged.Files) != 303 || !strings.Contains(strings.Join(staged.Notes, "\n"), ".cache/deps/passwd is not a regular file") {
		t.Fatalf("staged %d files, notes %v", len(staged.Files), staged.Notes)
	}
	dir := t.TempDir()
	archive, err := PackCache(staged.Files, filepath.Join(dir, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	if archive.Files != 303 || archive.Bytes > 200000 {
		t.Fatalf("the archive: %+v (3 MB of one letter and small files compress)", archive)
	}
	un, err := UnpackCache(ctx, archive.Path, filepath.Join(dir, "files"), []string{".cache/deps"}, UnpackLimits{})
	if err != nil || len(un.Files) != 303 {
		t.Fatalf("%+v %v", un, err)
	}
	if err := Place(ctx, ex, s2, un.Files, c.sink); err != nil {
		t.Fatalf("place: %v\n%s", err, c.text())
	}
	var c2 capture
	if code, err := ex.Run(ctx, s2, c2.sink); err != nil || code != 0 {
		t.Fatalf("step 2: %d %v\n%s", code, err, c2.text())
	}
	out := c2.text()
	for _, want := range []string{"stdout: 301", "stdout: 3000000", "stdout: file 299", "stdout: deep", "stdout: runs", "stdout: no-link"} {
		if !strings.Contains(out, want) {
			t.Fatalf("step 2 did not see %q:\n%s", want, out)
		}
	}
}

func TestHostCache(t *testing.T) {
	s1 := spec(t, cacheScript)
	s2 := spec(t, cacheCheck)
	cacheAcrossSteps(t, &Host{}, s1, s2)
}

func TestContainerCache(t *testing.T) {
	if testing.Short() || os.Getenv("LAUNCHWAY_TEST_CONTAINERS") == "" {
		t.Skip("set LAUNCHWAY_TEST_CONTAINERS=1 to run container tests")
	}
	c, err := DetectContainer()
	if err != nil {
		t.Skip(err)
	}
	s1 := spec(t, cacheScript)
	s2 := spec(t, cacheCheck)
	s2.Name += "-2"
	cacheAcrossSteps(t, c, s1, s2)
}

// The same on Kubernetes, where the workspace is in a pod: the files come
// out of the pod in one tar stream and go into another run's pod the same
// way.
func TestKubernetesCache(t *testing.T) {
	k := kube(t)
	ctx := context.Background()
	run1 := fmt.Sprintf("run_kc%d", time.Now().UnixNano())
	run2 := run1 + "b"
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(run1, k.owner), metav1.DeleteOptions{})
	defer k.client.CoreV1().Pods(k.o.Namespace).Delete(ctx, podName(run2, k.owner), metav1.DeleteOptions{})
	s1 := kspec(t, run1, "cache1", alpine, cacheScript, t.TempDir())
	s2 := kspec(t, run2, "cache2", alpine, cacheCheck, t.TempDir())
	s2.WorkspaceID = "ws2"
	cacheAcrossSteps(t, k, s1, s2)
}

// Whatever the globs match, the files of a .git directory are not kept in a
// cache: a broad glob must not take the repository's history and credentials
// into an entry every run may restore.
func TestStagingForACacheLeavesOutGitDirectories(t *testing.T) {
	ws := t.TempDir()
	for name, data := range map[string]string{
		".cache/deps/a.txt":         "a",
		".cache/deps/.git/config":   "[remote] url = https://token@example.com/x.git",
		".cache/deps/sub/.git/HEAD": "ref",
		"top.txt":                   "t",
		".git/config":               "secret",
	} {
		p := filepath.Join(ws, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Directories named .git are left out of a walk unless a glob names them;
	// for a cache, naming them is not enough.
	for _, globs := range [][]string{{".cache/deps", ".cache/deps/.git", ".cache/deps/sub/.git/HEAD"}, {"**/*", ".git/config", ".git"}} {
		staged, err := stageLocal(context.Background(), ws, globs, StageLimits{NoGit: true}.withDefaults(), t.TempDir())
		if err != nil {
			t.Fatalf("%v: %v", globs, err)
		}
		for _, f := range staged.Files {
			if inGitDir(f.Path) {
				t.Errorf("%v: %s is kept", globs, f.Path)
			}
		}
		if len(staged.Files) == 0 || !strings.Contains(strings.Join(staged.Notes, "\n"), "never kept") {
			t.Errorf("%v: %d files, notes %v", globs, len(staged.Files), staged.Notes)
		}
	}
	// Artifacts are not caches: they keep what their paths say.
	staged, err := stageLocal(context.Background(), ws, []string{".cache/deps", ".cache/deps/.git", ".cache/deps/sub/.git/HEAD"}, StageLimits{}.withDefaults(), t.TempDir())
	if err != nil || len(staged.Files) != 3 {
		t.Fatalf("%v %v", staged, err)
	}
}

func TestSecretLikeFiles(t *testing.T) {
	var files []StagedFile
	for _, p := range []string{".cache/a.txt", "x/.env", ".env.production", "keys/server.pem", "id_ed25519", "id_ed25519.pub", ".npmrc", "node_modules/y/index.js", "deploy/prod.tfvars", "cert.key"} {
		files = append(files, StagedFile{Path: p})
	}
	got := strings.Join(SecretLikeFiles(files), " ")
	want := "x/.env .env.production keys/server.pem id_ed25519 .npmrc deploy/prod.tfvars cert.key"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
