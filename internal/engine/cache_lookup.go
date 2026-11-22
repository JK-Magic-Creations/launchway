package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jk-magic-creations/launchway/internal/metrics"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// ---- looking up ----

// CacheLookup asks for the entry a step should restore.
type CacheLookup struct {
	Name     string `json:"name"`
	Key      string `json:"key"`      // 64 hex digits
	Platform string `json:"platform"` // linux/amd64
	// Fallback is "latest" to take the newest entry of the name and platform
	// that the run may read when none has the key.
	Fallback string `json:"fallback,omitempty"`
}

// CacheEntry is an entry a runner restores.
type CacheEntry struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Scope    string `json:"scope"`
	Platform string `json:"platform"`
	Bytes    int64  `json:"bytes"`
	Files    int    `json:"files"`
	SHA256   string `json:"sha256"`
	// Exact says the key is the one asked for; false for a fallback.
	Exact bool `json:"exact"`
	// Generation is which entry of its key this is, AgeSeconds how long ago
	// its contents were started from scratch, and Content a digest of what
	// it holds (see executor.ContentDigest).
	Generation int    `json:"generation"`
	AgeSeconds int64  `json:"age_seconds"`
	Content    string `json:"content,omitempty"`
}

// CacheLookupResult is the answer to a lookup: an entry, or nothing and,
// when it is not simply that none exists, why.
type CacheLookupResult struct {
	Entry *CacheEntry `json:"entry,omitempty"`
	// Reason says why there is no entry although one might: caching is
	// turned off, the server keeps no store.
	Reason string `json:"reason,omitempty"`
	// Expired is the entry with the key that was found too old to restore
	// (and nothing newer was restored in its place): the step rebuilds the
	// cache and saves it as the next generation.
	Expired *CacheEntry `json:"expired,omitempty"`
	// MaxAgeSeconds is the longest an entry may be, from when its contents
	// were started from scratch, for this cache.
	MaxAgeSeconds int64 `json:"max_age_seconds,omitempty"`
}

func checkCacheKey(key, platform string) error {
	if !validSHA256(key) {
		return fmt.Errorf("%w: a cache key is 64 lower-case hex digits", ErrInvalid)
	}
	if !cachePlatformRe.MatchString(platform) {
		return fmt.Errorf("%w: %q is not a platform such as linux/amd64", ErrInvalid, clipText(platform, 40))
	}
	return nil
}

// LookupCache finds the entry a step restores: the one with the key in the
// run's own scope, then in the default scope; with fallback latest, failing
// that the newest entry of the name and platform in the run's own scope and
// then in the default scope. Each hit counts as a use of the entry (it
// makes it newer for eviction).
func (e *Engine) LookupCache(leaseID string, in CacheLookup) (*CacheLookupResult, error) {
	if err := checkCacheKey(in.Key, in.Platform); err != nil {
		return nil, err
	}
	if in.Fallback != "" && in.Fallback != pipeline.CacheFallbackLatest && in.Fallback != pipeline.CacheFallbackNone {
		return nil, fmt.Errorf("%w: fallback is none or latest", ErrInvalid)
	}
	cc, err := e.cacheFor(leaseID, in.Name)
	if err != nil {
		return nil, err
	}
	if !cc.limits.enabled {
		return &CacheLookupResult{Reason: "caching is turned off for this project (cache policy)"}, nil
	}
	store, err := e.storeOrErr()
	if err != nil {
		return &CacheLookupResult{Reason: strings.TrimPrefix(err.Error(), ErrConflict.Error()+": ")}, nil
	}
	project := cc.st.Project
	d := cc.declared(in.Name)
	ns := cc.namespace(in.Name)
	now := e.now().UnixMilli()
	maxAge := cc.maxAge(in.Name)
	cutoff := now - maxAge.Milliseconds()
	// find looks the newest entry up in the scopes the run reads, in order:
	// of the key (the newest generation) or, not exact, of the name (the
	// newest made); fresh leaves out entries older than the maximum age.
	find := func(exact, fresh bool) (*CacheEntry, error) {
		for _, scope := range cc.scopes.readFor(d != nil && d.Sensitive) {
			q := `SELECT id, key, scope, platform, bytes, files, sha256, generation, born_at, content FROM caches
				WHERE project = ? AND scope = ? AND pipeline = ? AND (sensitive = 0 OR scope = ?) AND name = ? AND platform = ? AND status = ? AND store = ?`
			args := []any{project, scope, ns, cc.scopes.write, in.Name, in.Platform, cacheKept, store.Name()}
			if exact {
				q += ` AND key = ?`
				args = append(args, in.Key)
			}
			if fresh {
				q += ` AND born_at >= ?`
				args = append(args, cutoff)
			}
			if exact {
				q += ` ORDER BY generation DESC, created_at DESC, id DESC LIMIT 1`
			} else {
				q += ` ORDER BY created_at DESC, generation DESC, id DESC LIMIT 1`
			}
			en := &CacheEntry{Exact: exact}
			var born int64
			err := e.db.QueryRow(q, args...).Scan(&en.ID, &en.Key, &en.Scope, &en.Platform, &en.Bytes, &en.Files, &en.SHA256, &en.Generation, &born, &en.Content)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			en.AgeSeconds = max((now-born)/1000, 0)
			return en, nil
		}
		return nil, nil
	}
	res := &CacheLookupResult{MaxAgeSeconds: int64(maxAge / time.Second)}
	en, err := find(true, true)
	if err == nil && en == nil {
		// An entry with the key may be there and too old: it is not
		// restored, and the step is told, so that it is saved again.
		res.Expired, err = find(true, false)
		if err == nil && in.Fallback == pipeline.CacheFallbackLatest {
			en, err = find(false, true)
		}
	}
	if err != nil || en == nil {
		if err == nil {
			metrics.CacheLookups.Inc("miss")
		}
		return res, err
	}
	// The use counts only if the entry is still kept: another server may
	// have evicted it since it was read. Only an exact hit keeps the entry
	// from falling idle: a fallback is a starting point to be replaced, and
	// restoring it every run must not keep it alive.
	used := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		used = false
		q := `UPDATE caches SET hits = hits + 1, last_used_at = ? WHERE id = ? AND status = ?`
		args := []any{now, en.ID, cacheKept}
		if !en.Exact {
			q, args = `UPDATE caches SET hits = hits + 1 WHERE id = ? AND status = ?`, []any{en.ID, cacheKept}
		}
		r, err := tx.Exec(q, args...)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		used = n == 1
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !used {
		metrics.CacheLookups.Inc("miss")
		return &CacheLookupResult{MaxAgeSeconds: res.MaxAgeSeconds}, nil
	}
	metrics.CacheLookups.Inc("hit")
	res.Entry, res.Expired = en, nil
	return res, nil
}

// OpenCache opens an entry's bytes for the step attempt a lease holds, from
// offset, and keeps them until the reader is closed. Only entries that the step's own lookups may return: of the
// run's project, in a scope the run reads, under a name its step declares.
func (e *Engine) OpenCache(ctx context.Context, leaseID, id string, offset int64) (io.ReadCloser, int64, string, error) {
	// The download is counted before anything is checked, so that the
	// pruner cannot remove the bytes between the checks and the open.
	e.caches.startReading(id)
	r, size, sum, err := e.openCache(ctx, leaseID, id, offset)
	if err != nil {
		e.caches.stopReading(id)
		return nil, 0, "", err
	}
	return &cacheReader{ReadCloser: r, e: e, id: id, beat: e.now()}, size, sum, nil
}

// cacheReadBeat is how often a download in progress says so in the
// database, for the servers whose pruner has no count of it.
const cacheReadBeat = time.Minute

// cacheReader is a download of an entry's bytes. While it is open the
// entry's bytes are kept, even if the entry ends meanwhile: this server
// counts it, and others see its entry used, as removing waits for the
// entry to be idle for the grace period.
type cacheReader struct {
	io.ReadCloser
	e    *Engine
	id   string
	beat time.Time
	once sync.Once
}

func (r *cacheReader) Read(p []byte) (int, error) {
	if now := r.e.now(); now.Sub(r.beat) >= cacheReadBeat {
		r.beat = now
		r.e.touchCache(r.id, now.UnixMilli())
	}
	return r.ReadCloser.Read(p)
}

func (r *cacheReader) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(func() { r.e.caches.stopReading(r.id) })
	return err
}

// touchCache records that an entry's bytes are in use, for entries that have
// ended too (last_used_at is not otherwise looked at then). A failure only
// shortens the protection.
func (e *Engine) touchCache(id string, now int64) {
	if _, err := e.db.Exec(`UPDATE caches SET last_used_at = ? WHERE id = ? AND last_used_at < ?`, now, id, now); err != nil {
		e.log.Warn("recording that a cache download is in progress", "entry", id, "err", err)
	}
}

func (e *Engine) openCache(ctx context.Context, leaseID, id string, offset int64) (io.ReadCloser, int64, string, error) {
	row, err := cacheRow(e.db, id)
	if err != nil {
		return nil, 0, "", err
	}
	cc, err := e.cacheFor(leaseID, row.Name)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			err = fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
		}
		return nil, 0, "", err
	}
	if !cc.mayRead(row) || row.Status != cacheKept {
		return nil, 0, "", fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
	}
	if !cc.limits.enabled {
		return nil, 0, "", fmt.Errorf("%w: caching is turned off for this project", ErrNotFound)
	}
	if offset < 0 || offset > row.Bytes {
		return nil, 0, "", fmt.Errorf("%w: offset %d is outside the entry", ErrInvalid, offset)
	}
	store, err := e.storeOrErr()
	if err != nil {
		return nil, 0, "", err
	}
	if row.Store != store.Name() {
		return nil, 0, "", fmt.Errorf("%w: cache entry %s is kept in the %s store, which this server does not use", ErrNotFound, id, row.Store)
	}
	r, err := store.Open(ctx, row.BlobKey, offset, -1)
	if errors.Is(err, blobstore.ErrNotFound) {
		return nil, 0, "", fmt.Errorf("%w: the bytes of cache entry %s are no longer kept", ErrNotFound, id)
	}
	if err == nil {
		e.touchCache(id, e.now().UnixMilli())
	}
	return r, row.Bytes, row.SHA256, err
}

// DiscardCache ends an entry that a runner could not restore because its
// bytes are not what they were saved as: damaged on the way, or in the
// store, or not an archive Launchway made. Entries are never changed, so
// without this a damaged one would be found, fail and be found again until
// it fell idle. Only an entry the lease's step could restore may be
// discarded, only the run's own project's, and only one of the scope the run
// writes to.
func (e *Engine) DiscardCache(leaseID, id, why string) error {
	row, err := cacheRow(e.db, id)
	if err != nil {
		return err
	}
	cc, err := e.cacheFor(leaseID, row.Name)
	if err != nil {
		if errors.Is(err, ErrInvalid) {
			err = fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
		}
		return err
	}
	if !cc.mayRead(row) || row.Status != cacheKept {
		return fmt.Errorf("%w: cache entry %s", ErrNotFound, id)
	}
	// Reading an entry does not give the right to end it: a pull-request
	// run restores the default branch's entries, and must not be able to
	// remove them, only its own.
	if row.Scope != cc.scopes.write {
		return fmt.Errorf("%w: this run may not end the cache entry %s, which is in the %s scope; a run ends only entries of the scope it saves to", ErrForbidden, id, row.Scope)
	}
	e.log.Warn("a runner could not restore a cache entry and it is ended", "project", row.Project, "entry", id, "why", why)
	return e.endCacheByID(id, "runner", "could not be restored: "+cleanText(why, 150))
}
