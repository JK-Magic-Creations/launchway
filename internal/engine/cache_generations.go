package engine

import (
	"fmt"
	"time"
)

// Generations and maximum age (schema 35).
//
// An entry is never changed. A key whose cached paths changed during a step
// (an install that fetched a newer snapshot) is saved again as the next
// generation of the same key, by a step that started from the newest one; a
// restore takes the newest generation, and committing a generation ends the
// older ones of its key, which nothing restores any more.
//
// An entry is born when its contents were last started from scratch. One
// saved after a restore keeps the birth of the entry it started from, so a
// chain of refreshes (or of fallbacks, each a superset of the last) does not
// live for ever: once the birth is older than the maximum age a restore
// treats the entry as missing, the step rebuilds the cache from nothing, and
// that entry starts a new chain. The maximum age is the project's cache
// policy (7 days by default); a cache may ask for a shorter one, never a
// longer one.

// maxAge is how old, from its birth, an entry of the named cache may be and
// still be restored.
func (cc *cacheContext) maxAge(name string) time.Duration {
	age := cc.limits.maxAge
	if age <= 0 {
		age = time.Duration(defaultCachePolicy.MaxAgeDays) * 24 * time.Hour
	}
	if d := cc.declared(name); d != nil && d.MaxAge.D() > 0 && d.MaxAge.D() < age {
		age = d.MaxAge.D()
	}
	return age
}

// cacheBase is the entry a save says it started from, if the run may read it.
func (e *Engine) cacheBase(q querier, cc *cacheContext, id string) *cacheRowT {
	if id == "" {
		return nil
	}
	r, err := cacheRow(q, id)
	if err != nil || r.Status != cacheKept || !cc.mayRead(r) {
		return nil
	}
	return r
}

// endOlderGenerations ends the kept entries of a committed entry's key that
// are of an older generation: a restore takes the newest, so they only take
// room. Their bytes stay for the grace period, for a download in flight.
func endOlderGenerations(tx *txx, r *cacheRowT, now int64) error {
	rows, err := tx.Query(`SELECT id FROM caches WHERE project = ? AND scope = ? AND pipeline = ? AND name = ? AND key = ? AND platform = ?
		AND status = ? AND generation < ?`, r.Project, r.Scope, r.Pipeline, r.Name, r.Key, r.Platform, cacheKept, r.Generation)
	if err != nil {
		return err
	}
	var old []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		old = append(old, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range old {
		if _, _, err := endKeptCache(tx, id, "", fmt.Sprintf("replaced by generation %d", r.Generation), now); err != nil {
			return err
		}
	}
	return nil
}
