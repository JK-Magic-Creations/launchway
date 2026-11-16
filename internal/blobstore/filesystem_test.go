package blobstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var ctx = context.Background()

func newFS(t *testing.T) *Filesystem {
	t.Helper()
	fsys, err := NewFilesystem(filepath.Join(t.TempDir(), "store"))
	must(t, err)
	t.Cleanup(func() { fsys.Close() })
	return fsys
}

func TestFilesystemConformance(t *testing.T) {
	fsys := newFS(t)
	testStore(t, fsys)
	// Everything the suite made was deleted or completed: no uploads and
	// no temporary files are left behind.
	entries, err := os.ReadDir(filepath.Join(fsys.Dir(), uploadsDir))
	must(t, err)
	if len(entries) != 0 {
		t.Errorf("%d uploads left behind", len(entries))
	}
	assertNoTempFiles(t, fsys.Dir())
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tmpPrefix) {
			t.Errorf("temporary file left behind: %s", p)
		}
		return nil
	})
}

func TestNewFilesystem(t *testing.T) {
	if _, err := NewFilesystem(""); err == nil {
		t.Fatal("an empty directory was accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	must(t, os.WriteFile(file, nil, 0o600))
	if _, err := NewFilesystem(file); err == nil {
		t.Fatal("a regular file was accepted as the directory")
	}
	fsys := newFS(t)
	if fsys.Name() != "filesystem" {
		t.Fatalf("Name() = %q", fsys.Name())
	}
	for _, d := range []string{fsys.Dir(), filepath.Join(fsys.Dir(), objectsDir), filepath.Join(fsys.Dir(), uploadsDir)} {
		fi, err := os.Stat(d)
		must(t, err)
		if fi.Mode().Perm()&^0o750 != 0 {
			t.Errorf("%s has mode %v, want at most 0750", d, fi.Mode().Perm())
		}
	}
}

func TestFilesystemLayoutAndPermissions(t *testing.T) {
	fsys := newFS(t)
	key := "runs/1/out.txt"
	id, err := fsys.CreateUpload(ctx, key)
	must(t, err)
	if !validUploadID(id) {
		t.Fatalf("upload ID %q is not 32 hex digits", id)
	}
	etag, err := fsys.UploadPart(ctx, key, id, 1, []byte("hello"))
	must(t, err)
	if etag != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" { // sha256("hello")
		t.Fatalf("ETag %q is not the part's SHA-256", etag)
	}
	if _, err := os.Stat(filepath.Join(fsys.Dir(), uploadsDir, id, "1.part")); err != nil {
		t.Fatalf("the part is not where it should be: %v", err)
	}
	must(t, fsys.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: etag}}))
	fi, err := os.Stat(filepath.Join(fsys.Dir(), objectsDir, "runs", "1", "out.txt"))
	must(t, err)
	if fi.Mode().Perm()&^0o640 != 0 {
		t.Errorf("object has mode %v, want at most 0640", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(fsys.Dir(), uploadsDir, id)); !os.IsNotExist(err) {
		t.Errorf("the upload directory is still there after completing: %v", err)
	}
	r, err := fsys.Open(ctx, key, 1, 3)
	must(t, err)
	if _, ok := r.(*sectionFile); !ok {
		t.Errorf("Open returned %T, want a section of the file", r)
	}
	b := make([]byte, 10)
	n, _ := r.Read(b)
	must(t, r.Close())
	if string(b[:n]) != "ell" {
		t.Fatalf("read %q", b[:n])
	}
}

func TestFilesystemCompleteErrors(t *testing.T) {
	fsys := newFS(t)
	key := "a/b.bin"
	id, err := fsys.CreateUpload(ctx, key)
	must(t, err)
	e1, err := fsys.UploadPart(ctx, key, id, 1, []byte("one"))
	must(t, err)
	e2, err := fsys.UploadPart(ctx, key, id, 2, []byte("two"))
	must(t, err)

	err = fsys.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: e1}, {Number: 2, ETag: e1}})
	if err == nil || !strings.Contains(err.Error(), "part 2") {
		t.Fatalf("wrong ETag for part 2: got %v, want an error naming part 2", err)
	}
	err = fsys.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: e1}, {Number: 3, ETag: e2}})
	if err == nil || !strings.Contains(err.Error(), "part 3 ") {
		t.Fatalf("missing part 3: got %v, want an error naming part 3", err)
	}
	if err := fsys.CompleteUpload(ctx, key, id, []Part{{Number: 2, ETag: e2}, {Number: 1, ETag: e1}}); err == nil {
		t.Fatal("parts out of order were accepted")
	}
	if err := fsys.CompleteUpload(ctx, "a/other.bin", id, []Part{{Number: 1, ETag: e1}}); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("completing an upload into another key: got %v, want an error", err)
	}
	if _, err := fsys.UploadPart(ctx, "a/other.bin", id, 3, []byte("x")); err == nil {
		t.Fatal("uploading a part under another key was accepted")
	}
	if err := fsys.AbortUpload(ctx, "a/other.bin", id); err == nil {
		t.Fatal("aborting an upload under another key was accepted")
	}
	// The failures left nothing behind and the upload still completes.
	assertNoTempFiles(t, fsys.Dir())
	must(t, fsys.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: e1}, {Number: 2, ETag: e2}}))
	if got := readAll(t, fsys, key, 0, -1); string(got) != "onetwo" {
		t.Fatalf("got %q", got)
	}

	// A second upload to an existing key with other content is refused;
	// objects are never overwritten.
	id2, err := fsys.CreateUpload(ctx, key)
	must(t, err)
	e, err := fsys.UploadPart(ctx, key, id2, 1, []byte("something else"))
	must(t, err)
	if err := fsys.CompleteUpload(ctx, key, id2, []Part{{Number: 1, ETag: e}}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("overwriting with other content: got %v", err)
	}
	if got := readAll(t, fsys, key, 0, -1); string(got) != "onetwo" {
		t.Fatalf("the object changed to %q", got)
	}
	must(t, fsys.AbortUpload(ctx, key, id2))
}

func TestFilesystemConcurrentComplete(t *testing.T) {
	fsys := newFS(t)
	key := "race/object.bin"
	data := [][]byte{randomBytes(1 << 20), randomBytes(1 << 20), randomBytes(1000)}
	for round := range 5 {
		id, err := fsys.CreateUpload(ctx, key)
		must(t, err)
		var parts []Part
		for i, d := range data {
			etag, err := fsys.UploadPart(ctx, key, id, i+1, d)
			must(t, err)
			parts = append(parts, Part{Number: i + 1, ETag: etag})
		}
		// Several servers complete the same upload at once (a runner retried
		// against another server): all succeed, and the object is whole.
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = fsys.CompleteUpload(ctx, key, id, parts)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, completer %d: %v", round, i, err)
			}
		}
		if got := readAll(t, fsys, key, 0, -1); !bytes.Equal(got, bytes.Join(data, nil)) {
			t.Fatalf("round %d: the object is not the parts joined", round)
		}
		must(t, fsys.Delete(ctx, key))
	}
	assertNoTempFiles(t, fsys.Dir())
}

func TestFilesystemBadNames(t *testing.T) {
	fsys := newFS(t)
	keys := []string{"../x", "/x", "a//b", "A", "a\x00b", strings.Repeat("a", 513), "", "x/../../etc/passwd"}
	for _, key := range keys {
		if _, err := fsys.CreateUpload(ctx, key); err == nil {
			t.Errorf("CreateUpload(%q) succeeded", key)
		}
		if _, err := fsys.Open(ctx, key, 0, -1); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Open(%q): got %v, want a key error", key, err)
		}
		if _, err := fsys.Stat(ctx, key); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("Stat(%q): got %v, want a key error", key, err)
		}
		if err := fsys.Delete(ctx, key); err == nil {
			t.Errorf("Delete(%q) succeeded", key)
		}
	}
	for _, id := range []string{"../../etc", "", "ABCDEF0123456789ABCDEF0123456789", "0123456789abcdef0123456789abcde/", strings.Repeat("0", 33), "../" + strings.Repeat("0", 29)} {
		if _, err := fsys.UploadPart(ctx, "k", id, 1, []byte("x")); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("UploadPart with upload ID %q: got %v, want an ID error", id, err)
		}
		if err := fsys.CompleteUpload(ctx, "k", id, []Part{{Number: 1, ETag: "x"}}); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("CompleteUpload with upload ID %q: got %v, want an ID error", id, err)
		}
		if err := fsys.AbortUpload(ctx, "k", id); err == nil {
			t.Errorf("AbortUpload with upload ID %q succeeded", id)
		}
	}
	id, err := fsys.CreateUpload(ctx, "k")
	must(t, err)
	for _, n := range []int{0, -1, 10001} {
		if _, err := fsys.UploadPart(ctx, "k", id, n, []byte("x")); err == nil {
			t.Errorf("part number %d accepted", n)
		}
	}
	// A well-formed ID that was never handed out is an unknown upload.
	unknown := strings.Repeat("ab", 16)
	if _, err := fsys.UploadPart(ctx, "k", unknown, 1, []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown upload: got %v, want ErrNotFound", err)
	}
	must(t, fsys.AbortUpload(ctx, "k", unknown))
}

func TestFilesystemSymlinkCannotEscape(t *testing.T) {
	fsys := newFS(t)
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("outside"), 0o600))
	// Someone with access to the directory plants a link out of it.
	must(t, os.Symlink(outside, filepath.Join(fsys.Dir(), objectsDir, "evil")))
	if _, err := fsys.Open(ctx, "evil/secret", 0, -1); err == nil {
		t.Fatal("Open followed a symbolic link out of the store")
	}
	if err := fsys.Delete(ctx, "evil/secret"); err == nil {
		if _, serr := os.Stat(filepath.Join(outside, "secret")); serr != nil {
			t.Fatal("Delete removed a file outside the store")
		}
	}
}

func TestFilesystemMissingAndEmpty(t *testing.T) {
	fsys := newFS(t)
	if _, err := fsys.Stat(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat: %v", err)
	}
	if _, err := fsys.Open(ctx, "missing", 0, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open: %v", err)
	}
	must(t, fsys.Delete(ctx, "missing"))
	putObject(t, fsys, "dir/file", []byte("x"))
	// "dir" is a directory of objects, not an object.
	if _, err := fsys.Stat(ctx, "dir"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat of a directory: %v", err)
	}
	if _, err := fsys.Open(ctx, "dir", 0, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open of a directory: %v", err)
	}
	must(t, fsys.Delete(ctx, "dir"))
	// A path through a file is missing, not a failure.
	if _, err := fsys.Stat(ctx, "dir/file/below"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat through a file: %v", err)
	}
	// An empty object (a single empty part).
	putObject(t, fsys, "empty", nil)
	if size, err := fsys.Stat(ctx, "empty"); err != nil || size != 0 {
		t.Fatalf("empty object: %d, %v", size, err)
	}
	if got := readAll(t, fsys, "empty", 0, -1); len(got) != 0 {
		t.Fatalf("read %d bytes of an empty object", len(got))
	}
}

func TestFilesystemCancelledContext(t *testing.T) {
	fsys := newFS(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := fsys.CreateUpload(cctx, "k"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if err := fsys.Check(cctx); err == nil {
		t.Fatal("Check with a cancelled context succeeded")
	}
}

func TestFilesystemCheckLeavesNothing(t *testing.T) {
	fsys := newFS(t)
	must(t, fsys.Check(ctx))
	entries, err := os.ReadDir(filepath.Join(fsys.Dir(), objectsDir, strings.TrimSuffix(checkPrefix, "/")))
	must(t, err)
	if len(entries) != 0 {
		t.Fatalf("Check left %d files", len(entries))
	}
}
