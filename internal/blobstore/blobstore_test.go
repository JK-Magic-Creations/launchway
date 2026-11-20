package blobstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// testStore is the conformance suite every Store must pass. It runs against
// the filesystem store and the fake S3 server always, and against a real
// MinIO in the opt-in suite (s3_minio_test.go). Keys are fixed: callers give
// each run an empty store or a fresh prefix.
func testStore(t *testing.T, s Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Run("three parts", func(t *testing.T) {
		key := "conformance/three-parts.bin"
		parts := [][]byte{randomBytes(MinPartSize), randomBytes(MinPartSize), randomBytes(1024)}
		whole := bytes.Join(parts, nil)
		id, err := s.CreateUpload(ctx, key)
		must(t, err)
		// Parts may arrive in any order (at different servers).
		list := make([]Part, len(parts))
		for _, i := range []int{2, 0, 1} {
			etag, err := s.UploadPart(ctx, key, id, i+1, parts[i])
			must(t, err)
			list[i] = Part{Number: i + 1, ETag: etag}
		}
		must(t, s.CompleteUpload(ctx, key, id, list))
		t.Cleanup(func() { _ = s.Delete(context.Background(), key) })

		size, err := s.Stat(ctx, key)
		must(t, err)
		if size != int64(len(whole)) {
			t.Fatalf("size %d, want %d", size, len(whole))
		}
		if got := readAll(t, s, key, 0, -1); sha256.Sum256(got) != sha256.Sum256(whole) {
			t.Fatalf("the object read back differs from what was uploaded (%d bytes, want %d)", len(got), len(whole))
		}

		// A retry of the completion, after it succeeded, succeeds.
		must(t, s.CompleteUpload(ctx, key, id, list))

		// Ranged reads, across a part boundary and past the end.
		n := int64(len(whole))
		for _, r := range []struct{ off, length, wantFrom, wantTo int64 }{
			{0, 10, 0, 10},
			{5, 0, 5, 5},
			{MinPartSize - 3, 6, MinPartSize - 3, MinPartSize + 3},
			{n - 5, -1, n - 5, n},
			{n - 2, 100, n - 2, n},
			{n, -1, n, n},
			{n + 10, 5, n, n},
			{0, -1, 0, n},
		} {
			got := readAll(t, s, key, r.off, r.length)
			if !bytes.Equal(got, whole[r.wantFrom:r.wantTo]) {
				t.Errorf("Open(%d, %d): got %d bytes, want bytes %d..%d", r.off, r.length, len(got), r.wantFrom, r.wantTo)
			}
		}
		if _, err := s.Open(ctx, key, -1, 5); err == nil {
			t.Error("Open with a negative offset succeeded")
		}
	})

	t.Run("interrupted upload", func(t *testing.T) {
		key := "conformance/interrupted.bin"
		id, err := s.CreateUpload(ctx, key)
		must(t, err)
		_, err = s.UploadPart(ctx, key, id, 1, randomBytes(1000))
		must(t, err)
		// The uploader goes away; later the upload is aborted.
		must(t, s.AbortUpload(ctx, key, id))
		if _, err := s.UploadPart(ctx, key, id, 2, []byte("late")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("uploading to an aborted upload: got %v, want ErrNotFound", err)
		}
		if err := s.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: "x"}}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("completing an aborted upload: got %v, want ErrNotFound", err)
		}
		must(t, s.AbortUpload(ctx, key, id)) // unknown by now: not an error
		if _, err := s.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Stat after abort: got %v, want ErrNotFound", err)
		}
	})

	t.Run("part uploaded again", func(t *testing.T) {
		key := "conformance/replaced.bin"
		first, second := []byte("the first copy of part one"), []byte("the second, retried copy of part one")
		id, err := s.CreateUpload(ctx, key)
		must(t, err)
		etag1, err := s.UploadPart(ctx, key, id, 1, first)
		must(t, err)
		etag2, err := s.UploadPart(ctx, key, id, 1, second)
		must(t, err)
		if etag1 == etag2 {
			t.Fatal("two different parts got the same ETag")
		}
		if err := s.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: etag1}}); err == nil || errors.Is(err, ErrNotFound) {
			t.Fatalf("completing with the replaced part's ETag: got %v, want an error", err)
		}
		must(t, s.CompleteUpload(ctx, key, id, []Part{{Number: 1, ETag: etag2}}))
		t.Cleanup(func() { _ = s.Delete(context.Background(), key) })
		if got := readAll(t, s, key, 0, -1); !bytes.Equal(got, second) {
			t.Fatalf("got %q, want the latest part %q", got, second)
		}
	})

	t.Run("small parts", func(t *testing.T) {
		if s.Name() != "s3" {
			t.Skipf("the %s store does not enforce MinPartSize", s.Name())
		}
		key := "conformance/small-parts.bin"
		id, err := s.CreateUpload(ctx, key)
		must(t, err)
		defer s.AbortUpload(ctx, key, id)
		var parts []Part
		for n := 1; n <= 2; n++ {
			etag, err := s.UploadPart(ctx, key, id, n, randomBytes(1024))
			must(t, err)
			parts = append(parts, Part{Number: n, ETag: etag})
		}
		err = s.CompleteUpload(ctx, key, id, parts)
		var se *S3Error
		if !errors.As(err, &se) || se.Code != "EntityTooSmall" {
			t.Fatalf("completing with a small first part: got %v, want EntityTooSmall", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		key := "conformance/deleted.bin"
		putObject(t, s, key, []byte("short-lived"))
		must(t, s.Delete(ctx, key))
		if _, err := s.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Stat after Delete: got %v, want ErrNotFound", err)
		}
		if _, err := s.Open(ctx, key, 0, -1); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Open after Delete: got %v, want ErrNotFound", err)
		}
		must(t, s.Delete(ctx, key)) // missing: not an error
		if _, err := s.Open(ctx, "conformance/never-made", 0, 0); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Open of a missing object with length 0: got %v, want ErrNotFound", err)
		}
	})

	t.Run("concurrent uploads", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				key := fmt.Sprintf("conformance/concurrent/%d.bin", i)
				data := randomBytes(300<<10 + i)
				if err := upload(ctx, s, key, data); err != nil {
					errs <- fmt.Errorf("%s: %w", key, err)
					return
				}
				defer s.Delete(context.Background(), key)
				r, err := s.Open(ctx, key, 0, -1)
				if err != nil {
					errs <- err
					return
				}
				got, err := io.ReadAll(r)
				r.Close()
				if err != nil || !bytes.Equal(got, data) {
					errs <- fmt.Errorf("%s read back wrong (%d bytes, %v)", key, len(got), err)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
	})

	t.Run("check", func(t *testing.T) {
		must(t, s.Check(ctx))
	})

	t.Run("invalid keys", func(t *testing.T) {
		for _, key := range []string{"../x", "/x", "A"} {
			if _, err := s.CreateUpload(ctx, key); err == nil {
				t.Errorf("CreateUpload(%q) succeeded", key)
			}
			if _, err := s.Stat(ctx, key); err == nil || errors.Is(err, ErrNotFound) {
				t.Errorf("Stat(%q): got %v, want a key error", key, err)
			}
		}
	})
}

// upload stores data as key, in parts of MinPartSize.
func upload(ctx context.Context, s Store, key string, data []byte) error {
	id, err := s.CreateUpload(ctx, key)
	if err != nil {
		return err
	}
	var parts []Part
	for n := 1; ; n++ {
		chunk := data[:min(len(data), MinPartSize)]
		data = data[len(chunk):]
		etag, err := s.UploadPart(ctx, key, id, n, chunk)
		if err != nil {
			_ = s.AbortUpload(ctx, key, id)
			return err
		}
		parts = append(parts, Part{Number: n, ETag: etag})
		if len(data) == 0 {
			break
		}
	}
	if err := s.CompleteUpload(ctx, key, id, parts); err != nil {
		_ = s.AbortUpload(ctx, key, id)
		return err
	}
	return nil
}

func putObject(t *testing.T, s Store, key string, data []byte) {
	t.Helper()
	if err := upload(context.Background(), s, key, data); err != nil {
		t.Fatalf("uploading %s: %v", key, err)
	}
}

func readAll(t *testing.T, s Store, key string, off, length int64) []byte {
	t.Helper()
	r, err := s.Open(context.Background(), key, off, length)
	if err != nil {
		t.Fatalf("Open(%s, %d, %d): %v", key, off, length, err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading %s: %v", key, err)
	}
	return b
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateKey(t *testing.T) {
	good := []string{"a", "artifacts/run-1/step_2/out.tar.gz", "0", "a.b", "x/.hidden", strings.Repeat("a", MaxKeyLen)}
	for _, k := range good {
		if err := ValidateKey(k); err != nil {
			t.Errorf("ValidateKey(%q): %v", k, err)
		}
	}
	bad := []string{
		"", "../x", "a/../b", "a..b", "..", "/x", "x/", "a//b", "./a", "a/./b", ".",
		"A", "a b", "a\x00b", "a\\b", "a~b", "a:b", "é", "a\nb",
		strings.Repeat("a", MaxKeyLen+1),
	}
	for _, k := range bad {
		if err := ValidateKey(k); err == nil {
			t.Errorf("ValidateKey(%q) accepted a bad key", k)
		}
	}
}

func TestCheckParts(t *testing.T) {
	bad := [][]Part{
		nil,
		{{Number: 0, ETag: "e"}},
		{{Number: 10001, ETag: "e"}},
		{{Number: 2, ETag: "e"}, {Number: 1, ETag: "e"}},
		{{Number: 1, ETag: "e"}, {Number: 1, ETag: "e"}},
		{{Number: 1}},
	}
	for _, parts := range bad {
		if err := checkParts(parts); err == nil {
			t.Errorf("checkParts(%v) accepted", parts)
		}
	}
	must(t, checkParts([]Part{{Number: 1, ETag: "a"}, {Number: 3, ETag: "b"}, {Number: 10000, ETag: "c"}}))
}
