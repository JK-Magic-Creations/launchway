package storage

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// partSize is the size of the parts a pack is uploaded in.
const partSize = 16 << 20

// ErrCorrupt means a pack read back from the blob store is not the pack that
// was stored. The pack is refused and the repository flagged.
var ErrCorrupt = errors.New("a pack in the blob store does not match its recorded SHA-256")

// packKey is where a repository's pack lives. Packs are never shared between
// repositories, so the existence of an object cannot leak across tenants.
func packKey(repo, id string) string { return "forge/" + repo + "/packs/" + id + ".pack" }

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// objectCount reads the number of objects from a pack's index (version 2: the
// last entry of the fan-out table).
func objectCount(idxPath string) int64 {
	f, err := os.Open(idxPath)
	if err != nil {
		return 0
	}
	defer f.Close()
	var hdr [8 + 256*4]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[:4]) != "\xfftOc" {
		return 0
	}
	return int64(binary.BigEndian.Uint32(hdr[8+255*4:]))
}

// putPack uploads a pack file in parts and checks what was stored: its size
// always, and with verify its SHA-256 by reading it back.
func putPack(ctx context.Context, st blobstore.Store, repo, packPath string, verify bool) (Pack, error) {
	id, size, err := hashFile(packPath)
	if err != nil {
		return Pack{}, err
	}
	p := Pack{ID: id, BlobKey: packKey(repo, id), Bytes: size, Objects: objectCount(strings.TrimSuffix(packPath, ".pack") + ".idx")}
	if size == 0 {
		return Pack{}, errors.New("an empty pack")
	}
	// Named by its SHA-256 and immutable: if it is already there whole, it is this pack.
	if got, err := st.Stat(ctx, p.BlobKey); err == nil && got == size && !verify {
		return p, nil
	}
	up, err := st.CreateUpload(ctx, p.BlobKey)
	if err != nil {
		return Pack{}, err
	}
	abort := func() { _ = st.AbortUpload(context.WithoutCancel(ctx), p.BlobKey, up) }
	f, err := os.Open(packPath)
	if err != nil {
		abort()
		return Pack{}, err
	}
	defer f.Close()
	var parts []blobstore.Part
	buf := make([]byte, partSize)
	for n := 1; ; n++ {
		m, rerr := io.ReadFull(f, buf)
		if m > 0 {
			etag, err := st.UploadPart(ctx, p.BlobKey, up, n, buf[:m])
			if err != nil {
				abort()
				return Pack{}, fmt.Errorf("uploading part %d: %w", n, err)
			}
			parts = append(parts, blobstore.Part{Number: n, ETag: etag})
		}
		if rerr != nil {
			break
		}
	}
	if err := st.CompleteUpload(ctx, p.BlobKey, up, parts); err != nil {
		abort()
		return Pack{}, err
	}
	got, err := st.Stat(ctx, p.BlobKey)
	if err != nil {
		return Pack{}, err
	}
	if got != size {
		_ = st.Delete(ctx, p.BlobKey)
		return Pack{}, fmt.Errorf("%w: stored %d bytes of %d", ErrCorrupt, got, size)
	}
	if verify {
		sum, err := hashBlob(ctx, st, p.BlobKey)
		if err != nil {
			return Pack{}, err
		}
		if sum != id {
			_ = st.Delete(ctx, p.BlobKey)
			return Pack{}, fmt.Errorf("%w: read back %s", ErrCorrupt, short(sum))
		}
	}
	return p, nil
}

func hashBlob(ctx context.Context, st blobstore.Store, key string) (string, error) {
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

// getPack downloads a pack to dest and checks its SHA-256 against the
// manifest. A pack that is gone returns blobstore.ErrNotFound (the caller reads
// the manifest again: it was retired); one that is wrong returns ErrCorrupt.
func getPack(ctx context.Context, st blobstore.Store, p Pack, dest string) error {
	r, err := st.Open(ctx, p.BlobKey, 0, -1)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp := dest + ".part"
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

// putBytes stores a small object.
func putBytes(ctx context.Context, st blobstore.Store, key string, data []byte) error {
	up, err := st.CreateUpload(ctx, key)
	if err != nil {
		return err
	}
	etag, err := st.UploadPart(ctx, key, up, 1, data)
	if err != nil {
		_ = st.AbortUpload(ctx, key, up)
		return err
	}
	return st.CompleteUpload(ctx, key, up, []blobstore.Part{{Number: 1, ETag: etag}})
}
