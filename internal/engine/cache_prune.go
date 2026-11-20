package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
)

// ---- the pruner ----

// pruneCaches ends entries idle for longer than their project's policy
// says, uploads that stalled or whose step attempt is gone, and entries
// over a quota that was lowered, and removes the bytes of ended entries.
// Every server does this, possibly at the same moment, which is safe: every
// change of an entry is conditional on its status, so an entry is ended
// once and its bytes given back once, and the bytes of an ended entry are
// removed by the one server whose conditional update claims them (a claim
// whose server stopped is taken over after a while).
func (e *Engine) pruneCaches() {
	if !e.knows(schemaCache) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	now := e.now().UnixMilli()
	if err := e.expireCaches(now); err != nil {
		e.log.Warn("ending idle caches; trying again later", "err", err)
	}
	if err := e.failStaleCacheUploads(now); err != nil {
		e.log.Warn("ending abandoned cache uploads; trying again later", "err", err)
	}
	if err := e.trimCaches(); err != nil {
		e.log.Warn("trimming caches to their quota; trying again later", "err", err)
	}
	if e.arts.Store == nil {
		return
	}
	if err := e.removeCacheBytes(ctx, e.arts.Store, now); err != nil {
		e.log.Warn("removing the bytes of ended caches; trying again later", "err", err)
	}
	e.collectCacheStrays(ctx)
}

// expireCaches ends kept entries not restored for their project's idle
// days.
func (e *Engine) expireCaches(now int64) error {
	projects, err := e.idsOf(`SELECT DISTINCT project FROM caches WHERE status = ?`, cacheKept)
	if err != nil {
		return err
	}
	n := 0
	for _, project := range projects {
		lim, err := e.readCacheLimits(e.db, project)
		if err != nil {
			return err
		}
		cutoff := now - lim.idle.Milliseconds()
		list, err := e.idsOf(`SELECT id FROM caches WHERE project = ? AND status = ? AND last_used_at < ? ORDER BY last_used_at LIMIT ?`,
			project, cacheKept, cutoff, pruneBatchCaches)
		if err != nil {
			return err
		}
		for _, id := range list {
			err := e.artifactTx(func(tx *txx, now int64) error {
				if _, err := tx.Exec(`INSERT INTO cache_usage(project, bytes) VALUES(?, 0) ON CONFLICT(project) DO NOTHING`, project); err != nil {
					return err
				}
				k, _, err := endKeptCache(tx, id, "", fmt.Sprintf("not used for %d days", int(lim.idle/(24*time.Hour))), now)
				n += k
				return err
			})
			if err != nil {
				return err
			}
		}
	}
	if n > 0 {
		e.log.Info("idle caches ended", "count", n)
	}
	return nil
}

// failStaleCacheUploads ends uploads that made no progress for 48 hours,
// and uploads whose step attempt no runner holds any more.
func (e *Engine) failStaleCacheUploads(now int64) error {
	list, err := e.idsOf(`SELECT c.id FROM caches c WHERE c.status = ? AND (c.last_used_at < ? OR (c.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.run_id = c.created_run AND l.step_id = c.created_step AND l.attempt = c.created_attempt))) LIMIT ?`,
		cacheUploading, now-cacheStaleUpload.Milliseconds(), now-cacheOrphanedUpload.Milliseconds(), pruneBatchCaches)
	if err != nil {
		return err
	}
	for _, id := range list {
		r, err := cacheRow(e.db, id)
		if err != nil {
			continue
		}
		if err := e.artifactTx(func(tx *txx, now int64) error {
			_, err := endCacheTx(tx, r, "", "the upload did not complete", now)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// trimCaches ends the least recently used entries of projects that keep
// more than their quota allows, which happens when the quota is lowered.
func (e *Engine) trimCaches() error {
	rows, err := e.db.Query(`SELECT project, bytes FROM cache_usage WHERE bytes > 0`)
	if err != nil {
		return err
	}
	type use struct {
		project string
		bytes   int64
	}
	var all []use
	for rows.Next() {
		var u use
		if err := rows.Scan(&u.project, &u.bytes); err != nil {
			rows.Close()
			return err
		}
		all = append(all, u)
	}
	rows.Close()
	for _, u := range all {
		lim, err := e.readCacheLimits(e.db, u.project)
		if err != nil {
			return err
		}
		if u.bytes <= lim.maxProject {
			continue
		}
		err = e.artifactTx(func(tx *txx, now int64) error {
			var used int64
			if err := tx.QueryRow(`SELECT bytes FROM cache_usage WHERE project = ?`, u.project).Scan(&used); err != nil {
				return err
			}
			for used > lim.maxProject {
				var victim string
				err := tx.QueryRow(`SELECT id FROM caches WHERE project = ? AND status = ? ORDER BY last_used_at, created_at, id LIMIT 1`,
					u.project, cacheKept).Scan(&victim)
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				if err != nil {
					return err
				}
				_, freed, err := endKeptCache(tx, victim, "", "evicted: the project's caches were over their quota", now)
				if err != nil {
					return err
				}
				used -= freed
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// removeCacheBytes deletes the bytes of ended entries, and then their rows.
func (e *Engine) removeCacheBytes(ctx context.Context, store blobstore.Store, now int64) error {
	type cand struct {
		id, key, upload, status string
		endedAt                 int64
	}
	rows, err := e.db.Query(`SELECT id, blob_key, upload_id, status, ended_at FROM caches
		WHERE (status = ? AND ended_at < ?) OR (status = ? AND ended_at < ?) ORDER BY ended_at LIMIT ?`,
		cacheEnded, now-cacheEndedGrace.Milliseconds(), cacheRemoving, now-cacheOrphanedUpload.Milliseconds(), pruneBatchCaches)
	if err != nil {
		return err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.key, &c.upload, &c.status, &c.endedAt); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	removed := 0
	for _, c := range cands {
		var claimed bool
		err := e.artifactTx(func(tx *txx, now int64) error {
			res, err := tx.Exec(`UPDATE caches SET status = ?, ended_at = ? WHERE id = ? AND status = ? AND ended_at = ?`,
				cacheRemoving, now, c.id, c.status, c.endedAt)
			if err != nil {
				return err
			}
			k, _ := res.RowsAffected()
			claimed = k == 1
			return nil
		})
		if err != nil {
			return err
		}
		if !claimed {
			continue
		}
		if c.upload != "" {
			if err := store.AbortUpload(ctx, c.key, c.upload); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
				return fmt.Errorf("abandoning the upload of cache entry %s: %w", c.id, err)
			}
		}
		if err := store.Delete(ctx, c.key); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			return fmt.Errorf("deleting the bytes of cache entry %s: %w", c.id, err)
		}
		err = e.artifactTx(func(tx *txx, now int64) error {
			_, err := tx.Exec(`DELETE FROM caches WHERE id = ? AND status = ?`, c.id, cacheRemoving)
			return err
		})
		if err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		e.log.Info("removed the bytes of ended caches", "count", removed)
	}
	return nil
}

// collectCacheStrays deletes objects under caches/ that no entry refers to
// (left by a server that stopped between writing the bytes and recording
// the entry), in the database store, which can list its keys. Like
// removeStrays, it reads keys in pages from where the last pass stopped.
func (e *Engine) collectCacheStrays(ctx context.Context) {
	d, ok := e.arts.Store.(*blobstore.Database)
	if !ok {
		return
	}
	if err := e.removeCacheStrays(ctx, d); err != nil {
		e.log.Warn("removing objects of the database store that no cache entry refers to; trying again later", "err", err)
	}
}

func (e *Engine) removeCacheStrays(ctx context.Context, d *blobstore.Database) error {
	keys, err := d.Keys(ctx, "caches/", e.caches.strayAfter, pruneBatchCaches)
	if err != nil {
		return err
	}
	if len(keys) < pruneBatchCaches {
		e.caches.strayAfter = "" // start over next time
	} else {
		e.caches.strayAfter = keys[len(keys)-1]
	}
	cutoff := e.now().Add(-strayGrace).UnixMilli()
	removed := 0
	for _, k := range keys {
		var one int
		err := e.db.QueryRow(`SELECT 1 FROM caches WHERE blob_key = ?`, k).Scan(&one)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var created int64
		if err := e.db.QueryRow(`SELECT created_at FROM blob_objects WHERE key = ?`, k).Scan(&created); err != nil || created > cutoff {
			continue
		}
		if err := d.Delete(ctx, k); err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		e.log.Info("removed objects of the database store that no cache entry refers to", "count", removed)
	}
	return nil
}
