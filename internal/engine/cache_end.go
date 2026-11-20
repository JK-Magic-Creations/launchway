package engine

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/pipeline"
)

// ---- ending entries ----

// lockCacheUsage takes a project's usage row, which every change of the
// project's entries must do before it touches an entry: the usage row first,
// then entries, in every transaction (CommitCache and BeginCache through
// reserveCacheBytes, the pruner, deletion), so that servers working on the
// same project at once wait for each other in one order and cannot deadlock.
func lockCacheUsage(tx *txx, project string) error {
	if _, err := tx.Exec(`INSERT INTO cache_usage(project, bytes) VALUES(?, 0) ON CONFLICT(project) DO NOTHING`, project); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE cache_usage SET bytes = bytes WHERE project = ?`, project)
	return err
}

// endKeptCache ends a kept entry and gives its bytes back to its project's
// usage. It is conditional on the entry being kept, so of several servers
// ending one entry exactly one does, and the bytes are given back once. It
// returns how many entries it ended (0 or 1) and their size. It takes the
// project's usage row before the entry's (see lockCacheUsage), which is
// harmless for a caller that holds it already.
func endKeptCache(tx *txx, id, by, note string, now int64) (int, int64, error) {
	var project string
	err := tx.QueryRow(`SELECT project FROM caches WHERE id = ?`, id).Scan(&project)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if err := lockCacheUsage(tx, project); err != nil {
		return 0, 0, err
	}
	var bytes int64
	err = tx.QueryRow(`UPDATE caches SET status = ?, ended_at = ?, ended_by = ? WHERE id = ? AND status = ? RETURNING bytes`,
		cacheEnded, now, clipNote(by, note), id, cacheKept).Scan(&bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	_, err = tx.Exec(`UPDATE cache_usage SET bytes = CASE WHEN bytes > ? THEN bytes - ? ELSE 0 END WHERE project = ?`, bytes, bytes, project)
	return 1, bytes, err
}

func clipNote(by, note string) string {
	switch {
	case by == "":
		return cleanText(note, 200)
	case note == "":
		return by
	}
	return cleanText(by+": "+note, 200)
}

// endCacheByID ends an entry, kept or uploading.
func (e *Engine) endCacheByID(id, by, note string) error {
	r, err := cacheRow(e.db, id)
	if err != nil {
		return err
	}
	return e.artifactTx(func(tx *txx, now int64) error {
		_, err := endCacheTx(tx, r, by, note, now)
		return err
	})
}

// errCacheFull says a reservation of bytes did not fit in the project's
// quota even after every kept entry was ended. A transaction that gets it
// returns it, so that the entries it ended are not lost for nothing.
var errCacheFull = errors.New("the project's caches are full")

// cacheVictimSQL picks the entry to end first when a project is over its
// quota: the least recently used one of the scopes that are not the default
// branch's, and the default branch's only when nothing else is left. Many
// pull requests and branches, each writing its own scope, then cannot push
// the default branch's cache out, which every other run restores.
const cacheVictimSQL = `SELECT id FROM caches WHERE project = ? AND status = ?
	ORDER BY CASE WHEN scope = '` + cacheScopeDefault + `' THEN 1 ELSE 0 END, last_used_at, created_at, id LIMIT 1`

// reserveCacheBytes charges bytes to a project's usage, which counts the
// entries kept and the uploads in progress, ending entries (see
// cacheVictimSQL) until they fit within max. It says how many it ended and
// how many bytes they held, and whether the bytes now fit.
func reserveCacheBytes(tx *txx, project string, bytes, max, now int64) (evicted int, freed int64, fits bool, err error) {
	if err := lockCacheUsage(tx, project); err != nil {
		return 0, 0, false, err
	}
	for {
		res, err := tx.Exec(`UPDATE cache_usage SET bytes = bytes + ? WHERE project = ? AND bytes + ? <= ?`, bytes, project, bytes, max)
		if err != nil {
			return evicted, freed, false, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			return evicted, freed, true, nil
		}
		var victim string
		err = tx.QueryRow(cacheVictimSQL, project, cacheKept).Scan(&victim)
		if errors.Is(err, sql.ErrNoRows) {
			return evicted, freed, false, nil
		}
		if err != nil {
			return evicted, freed, false, err
		}
		n, size, err := endKeptCache(tx, victim, "", "evicted: the project's caches were over their quota", now)
		if err != nil {
			return evicted, freed, false, err
		}
		evicted += n
		freed += size
	}
}

// endCacheTx ends an entry in whichever of uploading or kept it is, and
// gives its bytes back to the project's usage either way.
func endCacheTx(tx *txx, r *cacheRowT, by, note string, now int64) (bool, error) {
	if err := lockCacheUsage(tx, r.Project); err != nil {
		return false, err
	}
	n, _, err := endKeptCache(tx, r.ID, by, note, now)
	if err != nil || n == 1 {
		return n == 1, err
	}
	var bytes int64
	err = tx.QueryRow(`UPDATE caches SET status = ?, ended_at = ?, ended_by = ? WHERE id = ? AND status = ? RETURNING bytes`,
		cacheEnded, now, clipNote(by, note), r.ID, cacheUploading).Scan(&bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// An upload was charged when it began (see BeginCache).
	_, err = tx.Exec(`UPDATE cache_usage SET bytes = CASE WHEN bytes > ? THEN bytes - ? ELSE 0 END WHERE project = ?`, bytes, bytes, r.Project)
	return true, err
}

// ---- listing and deleting ----

// CacheEntryView is an entry as listed.
type CacheEntryView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Key        string `json:"key"`
	Scope      string `json:"scope"`
	Platform   string `json:"platform"`
	Bytes      int64  `json:"bytes"`
	Files      int    `json:"files"`
	Status     string `json:"status"` // uploading or kept
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	Hits       int64  `json:"hits"`
	// RunID and RunNumber are the run that wrote it.
	RunID     string `json:"run_id"`
	RunNumber int    `json:"run_number"`
}

// CacheTotals compares what a project keeps with its quota.
type CacheTotals struct {
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// QuotaBytes is the most the project may keep, MaxEntryBytes the most
	// one entry may hold, and IdleDays how long an entry nobody restored is
	// kept.
	QuotaBytes    int64 `json:"quota_bytes"`
	MaxEntryBytes int64 `json:"max_entry_bytes"`
	IdleDays      int   `json:"idle_days"`
}

// CacheList is a project's caches.
type CacheList struct {
	Project string           `json:"project"`
	Entries []CacheEntryView `json:"entries"`
	Totals  CacheTotals      `json:"totals"`
	Policy  CachePolicyView  `json:"policy"`
}

// Caches lists a project's entries, newest use first, with what it keeps
// against its quota.
func (e *Engine) Caches(project string) (*CacheList, error) {
	if !e.knows(schemaCache) {
		return nil, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	lim, err := e.readCacheLimits(e.db, project)
	if err != nil {
		return nil, err
	}
	out := &CacheList{Project: project, Entries: []CacheEntryView{}, Policy: lim.view()}
	rows, err := e.db.Query(`SELECT c.id, c.name, c.key, c.scope, c.platform, c.bytes, c.files, c.status, c.created_at, c.last_used_at, c.hits,
		c.created_run, COALESCE(r.number, 0) FROM caches c LEFT JOIN runs r ON r.id = c.created_run
		WHERE c.project = ? AND c.status IN (?, ?) ORDER BY c.last_used_at DESC, c.created_at DESC, c.id DESC`, project, cacheKept, cacheUploading)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v CacheEntryView
		if err := rows.Scan(&v.ID, &v.Name, &v.Key, &v.Scope, &v.Platform, &v.Bytes, &v.Files, &v.Status, &v.CreatedAt, &v.LastUsedAt, &v.Hits,
			&v.RunID, &v.RunNumber); err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, v)
		// What counts against the quota is what is kept and what is being
		// uploaded.
		out.Totals.Bytes += v.Bytes
		if v.Status == cacheKept {
			out.Totals.Entries++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out.Totals.QuotaBytes, out.Totals.MaxEntryBytes, out.Totals.IdleDays = lim.maxProject, lim.maxEntry, int(lim.idle/(24*time.Hour))
	return out, nil
}

// CachesDeleted says what a delete ended.
type CachesDeleted struct {
	Ended int   `json:"ended"`
	Bytes int64 `json:"bytes"`
}

// DeleteCaches ends one entry of a project (by id), or every entry of a
// name (all scopes, keys and platforms). The next run that wanted one
// starts without it. Exactly one of id and name is given.
func (e *Engine) DeleteCaches(project, id, name, actor string) (*CachesDeleted, error) {
	if !e.knows(schemaCache) {
		return nil, fmt.Errorf("%w: this server does not know build caches (schema %d)", ErrConflict, schemaCache)
	}
	if (id == "") == (name == "") {
		return nil, fmt.Errorf("%w: say which entry (id) or which cache (name), not both and not neither", ErrInvalid)
	}
	var victims []*cacheRowT
	if id != "" {
		r, err := cacheRow(e.db, id)
		if err != nil || r.Project != project || (r.Status != cacheKept && r.Status != cacheUploading) {
			return nil, fmt.Errorf("%w: no cache entry %s in %s", ErrNotFound, clipText(id, 60), project)
		}
		victims = append(victims, r)
	} else {
		if !pipeline.ValidArtifactName(name) {
			return nil, fmt.Errorf("%w: %q is not a cache name", ErrInvalid, clipText(name, 60))
		}
		rows, err := e.db.Query(`SELECT `+cacheCols+` FROM caches WHERE project = ? AND name = ? AND status IN (?, ?)`, project, name, cacheKept, cacheUploading)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			r, err := scanCache(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			victims = append(victims, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(victims) == 0 {
			return nil, fmt.Errorf("%w: %s has no cache called %s", ErrNotFound, project, name)
		}
	}
	out := &CachesDeleted{}
	for _, r := range victims {
		err := e.artifactTx(func(tx *txx, now int64) error {
			ended, err := endCacheTx(tx, r, actor, "deleted", now)
			if ended {
				out.Ended++
				if r.Status == cacheKept {
					out.Bytes += r.Bytes
				}
			}
			return err
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
