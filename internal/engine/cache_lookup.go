package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

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
}

// CacheLookupResult is the answer to a lookup: an entry, or nothing and,
// when it is not simply that none exists, why.
type CacheLookupResult struct {
	Entry *CacheEntry `json:"entry,omitempty"`
	// Reason says why there is no entry although one might: caching is
	// turned off, the server keeps no store.
	Reason string `json:"reason,omitempty"`
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
	find := func(exact bool) (*CacheEntry, error) {
		for _, scope := range cc.scopes.read {
			q := `SELECT id, key, scope, platform, bytes, files, sha256 FROM caches
				WHERE project = ? AND scope = ? AND name = ? AND platform = ? AND status = ? AND store = ?`
			args := []any{project, scope, in.Name, in.Platform, cacheKept, store.Name()}
			if exact {
				q += ` AND key = ?`
				args = append(args, in.Key)
			}
			q += ` ORDER BY created_at DESC, id DESC LIMIT 1`
			en := &CacheEntry{Exact: exact}
			err := e.db.QueryRow(q, args...).Scan(&en.ID, &en.Key, &en.Scope, &en.Platform, &en.Bytes, &en.Files, &en.SHA256)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			return en, nil
		}
		return nil, nil
	}
	en, err := find(true)
	if err == nil && en == nil && in.Fallback == pipeline.CacheFallbackLatest {
		en, err = find(false)
	}
	if err != nil || en == nil {
		return &CacheLookupResult{}, err
	}
	// The hit counts only if the entry is still kept: another server may
	// have evicted it since it was read.
	used := false
	err = e.artifactTx(func(tx *txx, now int64) error {
		used = false
		res, err := tx.Exec(`UPDATE caches SET last_used_at = ?, hits = hits + 1 WHERE id = ? AND status = ?`, now, en.ID, cacheKept)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		used = n == 1
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !used {
		return &CacheLookupResult{}, nil
	}
	return &CacheLookupResult{Entry: en}, nil
}

// OpenCache opens an entry's bytes for the step attempt a lease holds, from
// offset. Only entries that the step's own lookups may return: of the
// run's project, in a scope the run reads, under a name its step declares.
func (e *Engine) OpenCache(ctx context.Context, leaseID, id string, offset int64) (io.ReadCloser, int64, string, error) {
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
	if row.Project != cc.st.Project || !cc.scopes.canRead(row.Scope) || row.Status != cacheKept {
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
	if row.Project != cc.st.Project || !cc.scopes.canRead(row.Scope) || row.Status != cacheKept {
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
