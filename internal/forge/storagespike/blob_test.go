package storagespike

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// PartSize is the size of the parts a pack is uploaded in.
const PartSize = 16 << 20

// ErrCorrupt means a pack read back from the blob store is not the pack that
// was stored: the repository is flagged, the pack refused.
var ErrCorrupt = errors.New("a pack in the blob store does not match its recorded SHA-256")

// FaultStore wraps a blob store with injected failures, to show how the push
// and the sync behave when the store is slow, unavailable, or wrong.
//
//	blob=down            every call fails
//	blob=slow:<dur>      every call takes that much longer
//	upload:part=error:N  the Nth part of an upload fails
//	upload:data=truncate the last part is stored cut in half, success reported
//	upload:data=flip     one byte of the first part is changed, success reported
type FaultStore struct {
	blobstore.Store
	F Fault
}

func (s *FaultStore) gate() error {
	if s.F.Is("blob", "down") {
		return fmt.Errorf("%w: the blob store is unavailable", ErrInjected)
	}
	if s.F.Is("blob", "slow") {
		d, _ := time.ParseDuration(s.F.Arg)
		time.Sleep(d)
	}
	return nil
}

// CreateUpload starts an upload.
func (s *FaultStore) CreateUpload(ctx context.Context, key string) (string, error) {
	if err := s.gate(); err != nil {
		return "", err
	}
	return s.Store.CreateUpload(ctx, key)
}

// UploadPart stores a part, or misbehaves.
func (s *FaultStore) UploadPart(ctx context.Context, key, id string, n int, data []byte) (string, error) {
	if err := s.gate(); err != nil {
		return "", err
	}
	if s.F.Is("upload:part", "error") && n == s.F.argInt() {
		return "", fmt.Errorf("%w: part %d of the upload", ErrInjected, n)
	}
	if s.F.Is("upload:data", "flip") && n == 1 && len(data) > 0 {
		data = append([]byte(nil), data...)
		data[len(data)/2] ^= 0xff
	}
	if s.F.Is("upload:data", "truncate") {
		data = data[:len(data)/2]
	}
	return s.Store.UploadPart(ctx, key, id, n, data)
}

// CompleteUpload finishes an upload.
func (s *FaultStore) CompleteUpload(ctx context.Context, key, id string, parts []blobstore.Part) error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.Store.CompleteUpload(ctx, key, id, parts)
}

// Open reads an object.
func (s *FaultStore) Open(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	if err := s.gate(); err != nil {
		return nil, err
	}
	return s.Store.Open(ctx, key, off, n)
}

// Stat returns an object's size.
func (s *FaultStore) Stat(ctx context.Context, key string) (int64, error) {
	if err := s.gate(); err != nil {
		return 0, err
	}
	return s.Store.Stat(ctx, key)
}

// Delete removes an object.
func (s *FaultStore) Delete(ctx context.Context, key string) error {
	if err := s.gate(); err != nil {
		return err
	}
	return s.Store.Delete(ctx, key)
}

// PackKey is where a repository's pack lives in the blob store. Packs are
// never shared between repositories.
func PackKey(repo, packID string) string { return "forge/" + repo + "/packs/" + packID + ".pack" }

// HashFile returns the SHA-256 and size of a file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// PutPack uploads a pack file in parts and checks the stored size. With
// verify it also reads the object back and checks its SHA-256, which is what
// catches an object store that reports success and keeps something else.
func PutPack(ctx context.Context, st blobstore.Store, repo, packPath string, verify bool) (PackInfo, error) {
	id, size, err := HashFile(packPath)
	if err != nil {
		return PackInfo{}, err
	}
	key := PackKey(repo, id)
	if got, err := st.Stat(ctx, key); err == nil && got == size {
		// Already there: a retry of an upload that finished. Immutable and
		// named by its SHA-256, so what is there is this pack.
		if !verify {
			return PackInfo{ID: id, BlobKey: key, Bytes: size}, nil
		}
	}
	up, err := st.CreateUpload(ctx, key)
	if err != nil {
		return PackInfo{}, err
	}
	abort := func() { _ = st.AbortUpload(context.WithoutCancel(ctx), key, up) }
	f, err := os.Open(packPath)
	if err != nil {
		abort()
		return PackInfo{}, err
	}
	defer f.Close()
	var parts []blobstore.Part
	buf := make([]byte, PartSize)
	for n := 1; ; n++ {
		m, rerr := io.ReadFull(f, buf)
		if m > 0 {
			etag, err := st.UploadPart(ctx, key, up, n, buf[:m])
			if err != nil {
				abort()
				return PackInfo{}, fmt.Errorf("uploading part %d: %w", n, err)
			}
			parts = append(parts, blobstore.Part{Number: n, ETag: etag})
		}
		if rerr != nil {
			break
		}
	}
	if len(parts) == 0 { // an empty pack cannot exist, but a store needs a part
		abort()
		return PackInfo{}, errors.New("an empty pack")
	}
	if err := st.CompleteUpload(ctx, key, up, parts); err != nil {
		abort()
		return PackInfo{}, err
	}
	got, err := st.Stat(ctx, key)
	if err != nil {
		return PackInfo{}, err
	}
	if got != size {
		_ = st.Delete(ctx, key)
		return PackInfo{}, fmt.Errorf("%w: stored %d bytes of %d", ErrCorrupt, got, size)
	}
	if verify {
		sum, err := hashObject(ctx, st, key)
		if err != nil {
			return PackInfo{}, err
		}
		if sum != id {
			_ = st.Delete(ctx, key)
			return PackInfo{}, fmt.Errorf("%w: read back %s", ErrCorrupt, short(sum))
		}
	}
	return PackInfo{ID: id, BlobKey: key, Bytes: size}, nil
}

func hashObject(ctx context.Context, st blobstore.Store, key string) (string, error) {
	r, err := st.Open(ctx, key, 0, -1)
	if err != nil {
		return "", err
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// GetPack downloads a pack to dest and checks its SHA-256 against the
// manifest. A pack that is gone returns blobstore.ErrNotFound (the caller
// re-reads the manifest: it was retired); one that is wrong returns
// ErrCorrupt.
func GetPack(ctx context.Context, st blobstore.Store, p PackInfo, dest string) error {
	r, err := st.Open(ctx, p.BlobKey, 0, -1)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := dest + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != p.ID {
		os.Remove(tmp)
		return fmt.Errorf("%w: %s", ErrCorrupt, short(p.ID))
	}
	return os.Rename(tmp, dest)
}
