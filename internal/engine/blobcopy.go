package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// Copying blobs between stores, for `launchway blobs copy`.
//
// The database is the list: every stored blob of artifact_blobs whose
// store is not the target is copied, in order of ID, and then recorded as
// kept in the target (artifact_blobs.store). That record is the progress:
// a copy that stops, for whatever reason, starts again where it was, and
// one run again later (after the servers switched stores) picks up what
// servers still on the old store kept meanwhile.
//
// Every copy is checked: the bytes read from the source must have the
// SHA-256 and size the database records for the blob before the target's
// upload is completed, and the target's copy is read back and checked
// again before the blob is recorded there. An object the target already
// has is checked the same way and never overwritten.
//
// Servers may keep running throughout. A blob that is being uploaded is
// not copied yet (run the copy again later). A blob removed while it is
// copied (its retention ended) is not recorded, and its copy is deleted
// from the target again. Servers that read from the source keep reading
// it: the copy leaves the source as it is unless asked to delete what it
// copied, which must wait until no server uses the source any more.

// CopyOptions configure a copy.
type CopyOptions struct {
	// DeleteSource deletes each blob from the source once it is recorded
	// in the target. Only once no server uses the source any more.
	DeleteSource bool
	// DryRun lists what would be copied without copying.
	DryRun bool
	// Progress, if set, is told about each blob.
	Progress func(format string, args ...any)
}

// CopyReport says what a copy did.
type CopyReport struct {
	Copied  int   `json:"copied"`  // written to the target and checked
	Present int   `json:"present"` // the target had them already, checked
	Bytes   int64 `json:"bytes"`   // copied
	// Missing are blobs the source does not have (kept in yet another
	// store, or lost); Vanished were removed while being copied; Failed
	// could not be copied (Errors says why, for the first few).
	Missing  int      `json:"missing"`
	Vanished int      `json:"vanished"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
	// Uploading counts blobs still being uploaded, not copied yet.
	Uploading int64 `json:"uploading"`
	// Left is what is still recorded in other stores afterwards.
	Left []StoreCount `json:"left"`
	// Marker says whether the store marker was copied (or was there).
	Marker bool `json:"marker"`
}

// copyPartBytes is the size of the parts a copy uploads: the engine's own,
// which every store accepts.
const copyPartBytes = ArtifactChunkBytes

// Copy copies the stored blobs not yet recorded in to from from to to.
func (a *BlobAdmin) Copy(ctx context.Context, from, to blobstore.Store, o CopyOptions) (*CopyReport, error) {
	if from.Name() == to.Name() {
		return nil, fmt.Errorf("the source and the target are both %s stores; copy between different kinds of store", from.Name())
	}
	say := o.Progress
	if say == nil {
		say = func(string, ...any) {}
	}
	rep := &CopyReport{Left: []StoreCount{}}
	fail := func(b BlobRef, err error) {
		rep.Failed++
		if len(rep.Errors) < 20 {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s (%s): %v", b.ID, b.Key, err))
		}
		say("FAILED %s: %v", b.Key, err)
	}
	if !o.DryRun {
		ok, err := a.copyMarker(ctx, from, to)
		if err != nil {
			return nil, fmt.Errorf("copying the store marker: %w", err)
		}
		rep.Marker = ok
	}
	after := ""
	for {
		list, err := a.StoredBlobs(ctx, to.Name(), after, 100)
		if err != nil {
			return rep, err
		}
		if len(list) == 0 {
			break
		}
		for _, b := range list {
			after = b.ID
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			if o.DryRun {
				say("would copy %s (%s)", b.Key, sizeText(b.Size))
				rep.Copied++
				rep.Bytes += b.Size
				continue
			}
			copied, err := copyBlob(ctx, from, to, b)
			switch {
			case errors.Is(err, errMissing):
				rep.Missing++
				say("not in the %s store: %s (recorded in %q)", from.Name(), b.Key, b.Store)
				continue
			case err != nil:
				fail(b, err)
				continue
			}
			ok, err := a.MarkCopied(ctx, b.ID, to.Name())
			if err != nil {
				return rep, err
			}
			if !ok {
				// Removed meanwhile: take the copy away again.
				_ = to.Delete(context.WithoutCancel(ctx), b.Key)
				rep.Vanished++
				continue
			}
			if copied {
				rep.Copied++
				rep.Bytes += b.Size
				say("copied %s (%s)", b.Key, sizeText(b.Size))
			} else {
				rep.Present++
				say("already there %s", b.Key)
			}
			if o.DeleteSource {
				if err := from.Delete(ctx, b.Key); err != nil {
					fail(b, fmt.Errorf("copied, but deleting it from the %s store: %w", from.Name(), err))
				}
			}
		}
	}
	var err error
	if rep.Uploading, err = a.Uploading(ctx); err != nil {
		return rep, err
	}
	left, err := a.Elsewhere(ctx, to.Name())
	if err != nil {
		return rep, err
	}
	rep.Left = append(rep.Left, left...)
	return rep, nil
}

// errMissing: the source does not have the blob.
var errMissing = errors.New("not in the source store")

// copyBlob makes sure to has b, checked. It reports whether it wrote it
// (false: it was there already).
func copyBlob(ctx context.Context, from, to blobstore.Store, b BlobRef) (bool, error) {
	if size, err := to.Stat(ctx, b.Key); err == nil {
		if size != b.Size {
			return false, fmt.Errorf("the %s store already has %s with %d bytes, not %d; it is left as it is", to.Name(), b.Key, size, b.Size)
		}
		if err := checkCopy(ctx, to, b); err != nil {
			return false, err
		}
		return false, nil
	} else if !errors.Is(err, blobstore.ErrNotFound) {
		return false, err
	}
	r, err := from.Open(ctx, b.Key, 0, -1)
	if errors.Is(err, blobstore.ErrNotFound) {
		return false, errMissing
	}
	if err != nil {
		return false, err
	}
	defer r.Close()
	up, err := to.CreateUpload(ctx, b.Key)
	if err != nil {
		return false, err
	}
	done := false
	defer func() {
		if !done {
			_ = to.AbortUpload(context.WithoutCancel(ctx), b.Key, up)
		}
	}()
	h := sha256.New()
	buf := make([]byte, copyPartBytes)
	var parts []blobstore.Part
	var total int64
	for n := 1; ; n++ {
		k, err := io.ReadFull(r, buf)
		if k > 0 {
			h.Write(buf[:k])
			total += int64(k)
			etag, err := to.UploadPart(ctx, b.Key, up, n, buf[:k])
			if err != nil {
				return false, err
			}
			parts = append(parts, blobstore.Part{Number: n, ETag: etag})
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return false, fmt.Errorf("reading it from the %s store: %w", from.Name(), err)
		}
	}
	if total != b.Size || hex.EncodeToString(h.Sum(nil)) != b.SHA256 {
		return false, fmt.Errorf("the %s store's copy is damaged: %d bytes with another SHA-256 than the %d bytes recorded; not copied", from.Name(), total, b.Size)
	}
	if err := to.CompleteUpload(ctx, b.Key, up, parts); err != nil {
		return false, err
	}
	done = true
	if err := checkCopy(ctx, to, b); err != nil {
		_ = to.Delete(context.WithoutCancel(ctx), b.Key)
		return false, err
	}
	return true, nil
}

// checkCopy reads b back from s and checks its SHA-256.
func checkCopy(ctx context.Context, s blobstore.Store, b BlobRef) error {
	r, err := s.Open(ctx, b.Key, 0, -1)
	if err != nil {
		return fmt.Errorf("reading it back from the %s store: %w", s.Name(), err)
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return fmt.Errorf("reading it back from the %s store: %w", s.Name(), err)
	}
	if n != b.Size || hex.EncodeToString(h.Sum(nil)) != b.SHA256 {
		return fmt.Errorf("the %s store's copy does not have the recorded SHA-256 (%d bytes)", s.Name(), n)
	}
	return nil
}

// copyMarker copies the store marker, so that servers using the target
// find it there. It reports whether the target has it afterwards.
func (a *BlobAdmin) copyMarker(ctx context.Context, from, to blobstore.Store) (bool, error) {
	key, err := a.Marker()
	if err != nil || key == "" {
		return false, err
	}
	if _, err := to.Stat(ctx, key); err == nil {
		return true, nil
	}
	r, err := from.Open(ctx, key, 0, 4096)
	if errors.Is(err, blobstore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return false, err
	}
	if err := putSmall(ctx, to, key, data); err != nil {
		return false, err
	}
	return true, nil
}
