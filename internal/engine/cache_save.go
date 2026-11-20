package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/ids"
)

// ---- saving ----

// BeginCache declares an entry to save.
type BeginCache struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Platform string `json:"platform"`
	Bytes    int64  `json:"bytes"`  // of the archive
	Files    int    `json:"files"`  // in it
	SHA256   string `json:"sha256"` // of the archive
}

// BegunCache says what to send. Exists says the entry is already kept (or
// being saved by another run): nothing is sent, and that is not an error.
type BegunCache struct {
	ID     string `json:"id,omitempty"`
	Exists bool   `json:"exists,omitempty"`
	Reason string `json:"reason,omitempty"`
	Scope  string `json:"scope,omitempty"`
	// Evicted is how many of the project's other entries were ended to make
	// room for the bytes this entry reserves.
	Evicted    int   `json:"evicted,omitempty"`
	Received   int64 `json:"received"`
	ChunkBytes int64 `json:"chunk_bytes"`
}

func cacheBytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/(1<<30))) + " GiB"
	default:
		return sizeText(n)
	}
}

// BeginCache declares an entry of the step attempt a lease holds, in the
// scope the server decides, or finds that it exists. A declaration the
// policy refuses (caching is off, the entry is over the limits, this run
// may not save) fails with ErrForbidden or ErrQuota, saying why in plain
// words; the runner logs it and the step's outcome does not change.
func (e *Engine) BeginCache(leaseID string, in BeginCache) (*BegunCache, error) {
	if err := checkCacheKey(in.Key, in.Platform); err != nil {
		return nil, err
	}
	switch {
	case in.Bytes <= 0:
		return nil, fmt.Errorf("%w: an entry holds at least one byte", ErrInvalid)
	case in.Files < 0 || in.Files > MaxCacheFiles:
		return nil, fmt.Errorf("%w: an entry holds at most %d files", ErrInvalid, MaxCacheFiles)
	case !validSHA256(in.SHA256):
		return nil, fmt.Errorf("%w: the SHA-256 must be 64 lower-case hex digits", ErrInvalid)
	}
	cc, err := e.cacheFor(leaseID, in.Name)
	if err != nil {
		return nil, err
	}
	switch {
	case !cc.limits.enabled:
		return nil, fmt.Errorf("%w: caching is turned off for this project (cache policy)", ErrForbidden)
	case cc.scopes.write == "":
		return nil, fmt.Errorf("%w: %s", ErrForbidden, cc.scopes.whyNone)
	case in.Bytes > cc.limits.maxEntry:
		return nil, fmt.Errorf("%w: this cache is %s; an entry may hold at most %s (cache policy max_entry_gib)",
			ErrQuota, cacheBytesText(in.Bytes), cacheBytesText(cc.limits.maxEntry))
	case in.Bytes > cc.limits.maxProject:
		return nil, fmt.Errorf("%w: this cache is %s; a project may keep at most %s of caches (cache policy max_project_gib)",
			ErrQuota, cacheBytesText(in.Bytes), cacheBytesText(cc.limits.maxProject))
	}
	store, err := e.storeOrErr()
	if err != nil {
		return nil, err
	}
	project, scope := cc.st.Project, cc.scopes.write
	same := func(r *cacheRowT) bool {
		return r.CreatedRun == cc.l.runID && r.CreatedStep == cc.l.stepID && r.CreatedAttempt == cc.l.attempt && r.SHA256 == in.SHA256 && r.Bytes == in.Bytes
	}
	// Entries are immutable: one that exists is not replaced.
	existing := func(q querier) (*cacheRowT, error) {
		r, err := scanCache(q.QueryRow(`SELECT `+cacheCols+` FROM caches WHERE project = ? AND scope = ? AND name = ? AND key = ? AND platform = ?
			AND status IN (?, ?)`, project, scope, in.Name, in.Key, in.Platform, cacheUploading, cacheKept))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return r, err
	}
	if r, err := existing(e.db); err != nil {
		return nil, err
	} else if r != nil && !(r.Status == cacheUploading && same(r)) {
		return existsAnswer(r, scope), nil
	}
	id := ids.New("cch")
	blobKey := "caches/" + strings.ReplaceAll(id, "_", "-")
	uploadID, err := store.CreateUpload(context.Background(), blobKey)
	if err != nil {
		return nil, fmt.Errorf("starting the upload of the cache: %w", err)
	}
	var out *BegunCache
	won := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		out, won = nil, false
		if err := leaseHeld(tx, leaseID); err != nil {
			return err
		}
		r, err := existing(tx)
		if err != nil {
			return err
		}
		if r != nil {
			if r.Status == cacheUploading && same(r) {
				// This attempt asked before and carries on.
				out = &BegunCache{ID: r.ID, Scope: scope, Received: r.Received, ChunkBytes: ArtifactChunkBytes}
				return nil
			}
			out = existsAnswer(r, scope)
			return nil
		}
		// The bytes are charged to the project's quota from here, until the
		// entry ends: what is being uploaded takes room as much as what is
		// kept, so that a loop of declarations cannot park more than the
		// quota allows.
		evicted, _, fits, err := reserveCacheBytes(tx, project, in.Bytes, cc.limits.maxProject, now)
		if err != nil {
			return err
		}
		if !fits {
			return errCacheFull
		}
		if _, err := tx.Exec(`INSERT INTO caches(id, project, scope, name, key, platform, blob_key, store, sha256, bytes, files, created_run, created_step,
			created_attempt, created_at, last_used_at, status, upload_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, project, scope, in.Name, in.Key, in.Platform, blobKey, store.Name(), in.SHA256, in.Bytes, in.Files, cc.l.runID, cc.l.stepID,
			cc.l.attempt, now, now, cacheUploading, uploadID); err != nil {
			return err
		}
		won = true
		out = &BegunCache{ID: id, Scope: scope, Evicted: evicted, ChunkBytes: ArtifactChunkBytes}
		return nil
	})
	if err != nil || !won {
		_ = store.AbortUpload(context.Background(), blobKey, uploadID)
	}
	if errors.Is(err, errCacheFull) {
		return nil, fmt.Errorf("%w: this cache is %s, which does not fit in the project's %s of caches, with the entries kept and the uploads in progress (cache policy max_project_gib)",
			ErrQuota, cacheBytesText(in.Bytes), cacheBytesText(cc.limits.maxProject))
	}
	return out, err
}

func existsAnswer(r *cacheRowT, scope string) *BegunCache {
	why := "an entry with this key was saved already, and entries are never changed"
	if r.Status == cacheUploading {
		why = "another run is saving an entry with this key"
	}
	return &BegunCache{Exists: true, Reason: why, Scope: scope, ChunkBytes: ArtifactChunkBytes}
}

// PutCacheChunk stores one chunk of an entry being saved: the bytes from
// offset, whose SHA-256 is sum. It returns how many bytes of the entry the
// server has. A chunk the server has already is accepted again without
// effect; one that does not start where the upload is gives a
// *ChunkOffsetError.
func (e *Engine) PutCacheChunk(ctx context.Context, leaseID, id string, offset int64, sum string, data []byte) (int64, error) {
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != sum {
		return 0, fmt.Errorf("%w: the chunk does not match its SHA-256; it was damaged on the way", ErrInvalid)
	}
	if len(data) == 0 || len(data) > ArtifactChunkBytes || offset < 0 || offset%ArtifactChunkBytes != 0 {
		return 0, fmt.Errorf("%w: a chunk starts at a multiple of %d bytes and holds 1 to %d bytes", ErrInvalid, ArtifactChunkBytes, ArtifactChunkBytes)
	}
	store, err := e.storeOrErr()
	if err != nil {
		return 0, err
	}
	r, cc, err := e.uploadingCache(leaseID, id)
	if err != nil {
		return 0, err
	}
	end := offset + int64(len(data))
	if end > r.Bytes || (end < r.Bytes && len(data) != ArtifactChunkBytes) {
		return 0, fmt.Errorf("%w: every chunk but the last holds %d bytes, and none goes past the entry's %d bytes", ErrInvalid, ArtifactChunkBytes, r.Bytes)
	}
	if r.Received >= end {
		return r.Received, nil // taken before
	}
	if offset != r.Received {
		return 0, &ChunkOffsetError{Received: r.Received}
	}
	h := sha256.New()
	if r.HashState != "" {
		raw, err := base64.StdEncoding.DecodeString(r.HashState)
		if err == nil {
			err = h.(encoding.BinaryUnmarshaler).UnmarshalBinary(raw)
		}
		if err != nil {
			return 0, fmt.Errorf("cache entry %s: reading the state of its SHA-256: %w", r.ID, err)
		}
	}
	h.Write(data)
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return 0, err
	}
	n := int(offset/ArtifactChunkBytes) + 1
	etag, err := store.UploadPart(ctx, r.BlobKey, r.UploadID, n, data)
	if errors.Is(err, blobstore.ErrFull) || errors.Is(err, blobstore.ErrTooLarge) {
		return 0, fmt.Errorf("%w: storing the cache: %v", ErrQuota, err)
	}
	if err != nil {
		return 0, fmt.Errorf("storing part %d of the cache: %w", n, err)
	}
	parts := append(slices.Clone(r.Parts), blobstore.Part{Number: n, ETag: etag})
	partsJSON, _ := json.Marshal(parts)
	moved := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		moved = false
		res, err := tx.Exec(`UPDATE caches SET received = ?, hash_state = ?, parts = ?, last_used_at = ? WHERE id = ? AND status = ? AND received = ?`,
			end, base64.StdEncoding.EncodeToString(state), string(partsJSON), now, r.ID, cacheUploading, r.Received)
		if err != nil {
			return err
		}
		k, _ := res.RowsAffected()
		moved = k == 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	if !moved {
		// Another server took the same chunk at the same moment, or the
		// entry ended.
		nr, err := cacheRow(e.db, r.ID)
		if err != nil {
			return 0, err
		}
		if nr.Status == cacheUploading && nr.Received >= end {
			return nr.Received, nil
		}
		if nr.Status != cacheUploading {
			return 0, fmt.Errorf("%w: the cache entry is %s", ErrConflict, nr.Status)
		}
		return 0, &ChunkOffsetError{Received: nr.Received}
	}
	if end < r.Bytes {
		return end, nil
	}
	if hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		// What arrived is not what was declared: give the entry up.
		_ = e.endCacheByID(r.ID, "", "the bytes received did not have the SHA-256 declared")
		_ = store.AbortUpload(ctx, r.BlobKey, r.UploadID)
		return 0, fmt.Errorf("%w: checksum mismatch: the archive does not have the SHA-256 it was declared with; save it again", ErrInvalid)
	}
	r.Received, r.Parts = end, parts
	_ = cc
	return end, e.completeCache(ctx, store, r)
}

// completeCache makes a fully received entry's object. Completing twice is
// harmless, so a server that finds an entry received but not completed
// (its server stopped in between) finishes the job.
func (e *Engine) completeCache(ctx context.Context, store blobstore.Store, r *cacheRowT) error {
	if err := store.CompleteUpload(ctx, r.BlobKey, r.UploadID, r.Parts); err != nil {
		if size, serr := store.Stat(ctx, r.BlobKey); serr != nil || size != r.Bytes {
			return fmt.Errorf("completing the upload: %w", err)
		}
	}
	return nil
}

// uploadingCache finds an entry being saved by the step attempt a lease
// holds.
func (e *Engine) uploadingCache(leaseID, id string) (*cacheRowT, *cacheContext, error) {
	r, err := cacheRow(e.db, id)
	if err != nil {
		return nil, nil, err
	}
	cc, err := e.cacheFor(leaseID, "")
	if err != nil {
		return nil, nil, err
	}
	if r.CreatedRun != cc.l.runID || r.CreatedStep != cc.l.stepID || r.CreatedAttempt != cc.l.attempt || r.Project != cc.st.Project {
		return nil, nil, fmt.Errorf("%w: cache entry %s is not of this step attempt", ErrNotFound, id)
	}
	if r.Status != cacheUploading {
		return nil, nil, fmt.Errorf("%w: cache entry %s is %s", ErrConflict, id, r.Status)
	}
	return r, cc, nil
}

// errCacheMoved says an entry changed under a commit's transaction.
var errCacheMoved = errors.New("the cache entry changed under the commit")

// CommittedCache says what committing did.
type CommittedCache struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
	// Exists says the entry was already committed: nothing changed.
	Exists bool `json:"exists,omitempty"`
}

// CommitCache makes an entry whose bytes have all arrived a kept one. Its
// bytes have counted against the project's quota since BeginCache reserved
// them, so committing cannot go over it. Two servers committing one entry
// at once: one wins, the other is told it exists.
func (e *Engine) CommitCache(ctx context.Context, leaseID, id string) (*CommittedCache, error) {
	store, err := e.storeOrErr()
	if err != nil {
		return nil, err
	}
	r, err := cacheRow(e.db, id)
	if err != nil {
		return nil, err
	}
	cc, err := e.cacheFor(leaseID, r.Name)
	if err != nil {
		return nil, err
	}
	if r.CreatedRun != cc.l.runID || r.CreatedStep != cc.l.stepID || r.CreatedAttempt != cc.l.attempt || r.Project != cc.st.Project {
		return nil, fmt.Errorf("%w: cache entry %s is not of this step attempt", ErrNotFound, id)
	}
	switch r.Status {
	case cacheKept:
		return &CommittedCache{ID: r.ID, Bytes: r.Bytes, Exists: true}, nil
	case cacheUploading:
	default:
		return nil, fmt.Errorf("%w: cache entry %s is %s", ErrConflict, id, r.Status)
	}
	if !cc.limits.enabled {
		_ = e.endCacheByID(r.ID, "", "caching was turned off while it was saved")
		return nil, fmt.Errorf("%w: caching is turned off for this project (cache policy)", ErrForbidden)
	}
	if r.Received != r.Bytes {
		return nil, fmt.Errorf("%w: not every byte has been sent yet: the server has %d of %d", ErrConflict, r.Received, r.Bytes)
	}
	if err := e.completeCache(ctx, store, r); err != nil {
		return nil, err
	}
	var out *CommittedCache
	err = e.artifactTx(func(tx *txx, now int64) error {
		out = nil
		if err := leaseHeld(tx, leaseID); err != nil {
			return err
		}
		// The entry's bytes were charged to the project when it began (see
		// BeginCache), so committing only makes it kept.
		res, err := tx.Exec(`UPDATE caches SET status = ?, last_used_at = ?, hash_state = '', parts = '[]' WHERE id = ? AND status = ?`,
			cacheKept, now, r.ID, cacheUploading)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Another server committed it first (or it ended).
			return errCacheMoved
		}
		out = &CommittedCache{ID: r.ID, Bytes: r.Bytes}
		return nil
	})
	if err != nil {
		if errors.Is(err, errCacheMoved) || isConflict(err) {
			// Two committers of the same entry: whoever lost sees it kept.
			if cur, gerr := cacheRow(e.db, id); gerr == nil && cur.Status == cacheKept {
				return &CommittedCache{ID: r.ID, Bytes: r.Bytes, Exists: true}, nil
			} else if gerr == nil {
				return nil, fmt.Errorf("%w: cache entry %s is %s", ErrConflict, id, cur.Status)
			}
		}
		return nil, err
	}
	return out, nil
}
