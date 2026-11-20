package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jk-magic-creations/launchway/internal/blobstore"
	"github.com/jk-magic-creations/launchway/internal/ids"
)

// The life of artifacts after they are kept: what ends them (their step
// attempt was lost, their retention ran out, someone deleted them), and
// removing contents nothing refers to any more.

// endArtifact moves an artifact out of uploading or kept, giving its bytes
// back to its project's quota. It is conditional on the status, so of
// several servers ending the same artifact at once exactly one does.
func endArtifact(tx *txx, id, status, by, note string, now int64) (bool, error) {
	var project string
	var bytes int64
	err := tx.QueryRow(`UPDATE artifacts SET status = ?, ended_at = ?, ended_by = ?, note = ?
		WHERE id = ? AND status IN (?, ?) RETURNING project, bytes`,
		status, now, by, cleanText(note, maxReportText), id, artUploading, artKept).Scan(&project, &bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(`UPDATE artifact_usage SET bytes = CASE WHEN bytes > ? THEN bytes - ? ELSE 0 END WHERE project = ?`, bytes, bytes, project)
	return err == nil, err
}

// endAttemptArtifacts ends the artifacts of a step attempt that are in one
// of the statuses.
func endAttemptArtifacts(tx *txx, run, step string, attempt int, statuses []string, to, note string, now int64) error {
	rows, err := tx.Query(`SELECT id, status FROM artifacts WHERE run_id = ? AND step_id = ? AND attempt = ?`, run, step, attempt)
	if err != nil {
		return err
	}
	var list []string
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return err
		}
		if slices.Contains(statuses, status) {
			list = append(list, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range list {
		if _, err := endArtifact(tx, id, to, "", note, now); err != nil {
			return err
		}
	}
	return nil
}

// attemptArtifacts lists the artifacts kept for a step attempt, for the
// event that ends it.
func attemptArtifacts(q querier, run, step string, attempt int) ([]ArtifactSummary, error) {
	rows, err := q.Query(`SELECT id, name, kind, files, bytes FROM artifacts WHERE run_id = ? AND step_id = ? AND attempt = ? AND status = ?
		ORDER BY name`, run, step, attempt, artKept)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtifactSummary
	for rows.Next() {
		var a ArtifactSummary
		if err := rows.Scan(&a.ID, &a.Name, &a.Kind, &a.Files, &a.Bytes); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteArtifact deletes a kept artifact of a run, as actor. Its contents
// are removed once nothing else refers to them.
func (e *Engine) DeleteArtifact(runID, artifactID, actor string) (*ArtifactView, error) {
	if !e.knows(schemaArtifacts) {
		return nil, fmt.Errorf("%w: artifact %s", ErrNotFound, artifactID)
	}
	var out *ArtifactView
	err := e.artifactTx(func(tx *txx, now int64) error {
		a, err := artifactByID(tx, artifactID)
		if err != nil {
			return err
		}
		if a.RunID != runID {
			return fmt.Errorf("%w: artifact %s", ErrNotFound, artifactID)
		}
		if a.Status != artKept {
			return fmt.Errorf("%w: artifact %s is %s", ErrConflict, a.Name, a.statusText())
		}
		ok, err := endArtifact(tx, a.ID, artDeleted, actor, "", now)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: artifact %s changed while it was deleted", ErrConflict, a.Name)
		}
		a.Status, a.EndedAt, a.EndedBy = artDeleted, now, actor
		out = a.view(nil)
		return nil
	})
	return out, err
}

// pruneBatchArtifacts bounds what one pass handles of each kind.
const pruneBatchArtifacts = 200

// pruneArtifacts ends artifacts whose retention ran out and uploads that
// were abandoned, and removes contents nothing refers to. Every server
// does this; they may do it at the same moment, which is safe:
//
//   - Ending an artifact is a conditional update (endArtifact): one server
//     does it, and gives its bytes back to the quota once.
//   - A blob is removed in two steps. First a conditional update marks it
//     deleting, only if it is stored, unused for blobGraceMs and no
//     uploading or kept artifact refers to it; only the server whose update
//     changed the row goes on to delete the object and then the row. A
//     runner declaring or committing a file with that content refreshes
//     used_at with an update conditional on the blob being stored, so either
//     the refresh comes first (the blob is then recently used and not
//     marked) or the mark does (the refresh changes nothing, and the file
//     is sent again).
//   - A blob marked deleting whose server stopped before finishing is
//     finished by any server later: deleting an object twice is harmless.
func (e *Engine) pruneArtifacts() {
	defer e.pruneCaches() // build caches (schema 25) are pruned at the same time
	if !e.knows(schemaArtifacts) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	now := e.now().UnixMilli()
	if err := e.expireArtifacts(now); err != nil {
		e.log.Warn("ending expired artifacts; trying again later", "err", err)
	}
	if err := e.failStaleUploads(now); err != nil {
		e.log.Warn("ending abandoned artifact uploads; trying again later", "err", err)
	}
	if e.arts.Store == nil {
		return
	}
	if err := e.removeBlobs(ctx, e.arts.Store, now); err != nil {
		e.log.Warn("removing artifact contents nothing refers to; trying again later", "err", err)
	}
	e.collectStore(ctx)
}

func (e *Engine) idsOf(q string, args ...any) ([]string, error) {
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// expireArtifacts ends kept artifacts whose retention has run out, except
// those of runs still going, which a later step may still fetch.
func (e *Engine) expireArtifacts(now int64) error {
	list, err := e.idsOf(`SELECT a.id FROM artifacts a JOIN runs r ON r.id = a.run_id
		WHERE a.status = ? AND a.expires_at <= ? AND r.status NOT IN (?, ?) ORDER BY a.expires_at LIMIT ?`,
		artKept, now, RunQueued, RunRunning, pruneBatchArtifacts)
	if err != nil {
		return err
	}
	n := 0
	for _, id := range list {
		err := e.artifactTx(func(tx *txx, now int64) error {
			ok, err := endArtifact(tx, id, artExpired, "", "", now)
			if ok {
				n++
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	if n > 0 {
		e.log.Info("artifacts expired", "count", n)
	}
	return nil
}

// failStaleUploads ends artifacts still uploading whose step attempt no
// runner holds any more (its lease ended without the step finishing, for
// example because the server stopped), after a while.
func (e *Engine) failStaleUploads(now int64) error {
	list, err := e.idsOf(`SELECT a.id FROM artifacts a WHERE a.status = ? AND a.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM leases l WHERE l.run_id = a.run_id AND l.step_id = a.step_id AND l.attempt = a.attempt)
		LIMIT ?`, artUploading, now-int64(10*time.Minute/time.Millisecond), pruneBatchArtifacts)
	if err != nil {
		return err
	}
	for _, id := range list {
		if err := e.artifactTx(func(tx *txx, now int64) error {
			_, err := endArtifact(tx, id, artFailed, "", "the upload did not complete before the step attempt ended", now)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// unreferenced is the condition that no uploading or kept artifact refers
// to the blob b.
const unreferenced = `NOT EXISTS (SELECT 1 FROM artifact_files f JOIN artifacts a ON a.id = f.artifact_id
	WHERE f.blob_id = b.id AND a.status IN ('uploading', 'kept'))`

// removeBlobs removes the contents nothing refers to, and uploads that
// stopped making progress.
func (e *Engine) removeBlobs(ctx context.Context, store blobstore.Store, now int64) error {
	type cand struct{ id, key, upload, state string }
	rows, err := e.db.Query(`SELECT b.id, b.store_key, b.upload_id, b.state FROM artifact_blobs b
		WHERE (b.state = ? AND b.used_at < ? AND `+unreferenced+`)
		   OR (b.state = ? AND b.used_at < ?)
		   OR (b.state = ? AND b.used_at < ?)
		ORDER BY b.used_at LIMIT ?`,
		blobStored, now-blobGraceMs, blobUploading, now-staleUploadMs, blobDeleting, now-int64(10*time.Minute/time.Millisecond),
		pruneBatchArtifacts)
	if err != nil {
		return err
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.key, &c.upload, &c.state); err != nil {
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
		if c.state != blobDeleting {
			// Claim it; only the server whose update changes the row goes on.
			var claimed bool
			err := e.artifactTx(func(tx *txx, now int64) error {
				q := `UPDATE artifact_blobs SET state = ?, used_at = ? WHERE id = ? AND state = ? AND used_at < ? AND ` + unreferencedB
				cutoff := now - blobGraceMs
				if c.state == blobUploading {
					q = `UPDATE artifact_blobs SET state = ?, used_at = ? WHERE id = ? AND state = ? AND used_at < ?`
					cutoff = now - staleUploadMs
				}
				res, err := tx.Exec(q, blobDeleting, now, c.id, c.state, cutoff)
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				claimed = n == 1
				if claimed && c.state == blobUploading {
					// Files of artifacts still uploading start it again.
					_, err = tx.Exec(`UPDATE artifact_files SET blob_id = '' WHERE blob_id = ?`, c.id)
				}
				return err
			})
			if err != nil {
				return err
			}
			if !claimed {
				continue
			}
		}
		if c.upload != "" {
			if err := store.AbortUpload(ctx, c.key, c.upload); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
				return fmt.Errorf("abandoning the upload of blob %s: %w", c.id, err)
			}
		}
		if err := store.Delete(ctx, c.key); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			return fmt.Errorf("deleting blob %s: %w", c.id, err)
		}
		err := e.artifactTx(func(tx *txx, now int64) error {
			_, err := tx.Exec(`DELETE FROM artifact_blobs WHERE id = ? AND state = ?`, c.id, blobDeleting)
			return err
		})
		if err != nil {
			return err
		}
		removed++
	}
	if removed > 0 {
		e.log.Info("removed artifact contents nothing refers to", "count", removed)
	}
	return nil
}

// unreferencedB is unreferenced for an UPDATE of artifact_blobs, whose row
// is not aliased there.
const unreferencedB = `NOT EXISTS (SELECT 1 FROM artifact_files f JOIN artifacts a ON a.id = f.artifact_id
	WHERE f.blob_id = artifact_blobs.id AND a.status IN ('uploading', 'kept'))`

// checkArtifactStore makes sure this server's blob store is the one the
// other servers sharing the database use, by keeping a small marker object
// whose key is in the database. A server whose store does not have it (a
// directory that is not shared, another bucket or prefix) keeps no
// artifacts and says why, instead of keeping some where other servers
// cannot find them.
//
// Open calls it before the engine starts its background work, so nothing
// else reads the store setting while it may change.
func (e *Engine) checkArtifactStore() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := e.arts.Store
	if store == nil || !e.knows(schemaArtifacts) {
		return nil
	}
	if err := store.Check(ctx); err != nil {
		return e.disableArtifacts(fmt.Sprintf("the %s artifact store does not work: %v", store.Name(), err))
	}
	var key string
	err := e.db.QueryRow(`SELECT value FROM meta WHERE key = 'artifact_store_marker'`).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		mine := "launchway-marker/" + ids.New("m")
		if err := putSmall(ctx, store, mine, []byte("This object tells Launchway servers that they share this store. Keep it.\n")); err != nil {
			return e.disableArtifacts(fmt.Sprintf("the %s artifact store cannot be written: %v", store.Name(), err))
		}
		unlock := e.writeLock()
		_, err = e.db.Exec(`INSERT INTO meta(key, value) VALUES('artifact_store_marker', ?) ON CONFLICT(key) DO NOTHING`, mine)
		unlock()
		if err != nil {
			return err
		}
		if err := e.db.QueryRow(`SELECT value FROM meta WHERE key = 'artifact_store_marker'`).Scan(&key); err != nil {
			return err
		}
		if key != mine {
			_ = store.Delete(ctx, mine)
		}
	} else if err != nil {
		return err
	}
	if _, err := store.Stat(ctx, key); err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return e.markerMissing(ctx, store, key)
		}
		return e.disableArtifacts(fmt.Sprintf("the %s artifact store cannot be read: %v", store.Name(), err))
	}
	return nil
}

// markerMissing handles a store that lacks the marker the database
// records. The database store is shared by every server by definition, so
// it becomes the marked store: the servers move to it together, and files
// recorded in the store used before are not forgotten (the server says
// where they are, and every download of one says how to copy them). Any
// other store lacking the marker is not where the artifacts are: another
// server's unshared directory, or the store before artifacts.store was
// changed. It keeps no artifacts and says which, and how to move them.
func (e *Engine) markerMissing(ctx context.Context, store blobstore.Store, key string) error {
	name := store.Name()
	var elsewhere []StoreCount
	if e.knows(schemaBlobs) {
		var err error
		if elsewhere, err = e.blobsElsewhere(name); err != nil {
			return err
		}
	}
	if _, ok := store.(*blobstore.Database); ok {
		mine, err := e.adoptMarker(ctx, store, key)
		if err != nil {
			return e.disableArtifacts(fmt.Sprintf("the database artifact store cannot be written: %v", err))
		}
		if _, err := store.Stat(ctx, mine); err != nil {
			return e.disableArtifacts(fmt.Sprintf("this server's database artifact store lacks the marker %s that another server just wrote (%v)", mine, err))
		}
		if len(elsewhere) > 0 {
			e.log.Warn("artifacts kept before are in another store; this server cannot read them until they are copied into the database",
				"kept_elsewhere", describeElsewhere(elsewhere), "copy_with", "launchway blobs copy --from filesystem|s3 --to database")
		}
		return nil
	}
	why := fmt.Sprintf("this server's %s artifact store is not the one the other servers use (it lacks their marker %s); "+
		"several servers need one shared store", name, key)
	if len(elsewhere) > 0 {
		from := elsewhere[0].Store
		if from == "" {
			from = "filesystem"
		}
		why = fmt.Sprintf("this server's %s artifact store does not hold the artifacts kept so far (it lacks the marker %s): "+
			"the database records %s. If artifacts.store was changed, copy them over with `launchway blobs copy --from %s --to %s` "+
			"(servers may keep running), or set artifacts.store back", name, key, describeElsewhere(elsewhere), from, name)
		if name == "filesystem" {
			why += ". If several servers each have a directory of their own, use the database store (leave artifacts.store unset on PostgreSQL), " +
				"which needs nothing extra, or S3"
		}
	} else if name == "filesystem" {
		why += ": leave artifacts.store unset (on PostgreSQL the database keeps them), or use S3 or a shared directory"
	}
	return e.disableArtifacts(why)
}

func (e *Engine) disableArtifacts(why string) error {
	e.log.Error("artifacts are turned off on this server", "reason", why)
	e.arts.Store, e.arts.StoreProblem = nil, why
	return nil
}

// putSmall writes a small object.
func putSmall(ctx context.Context, store blobstore.Store, key string, data []byte) error {
	up, err := store.CreateUpload(ctx, key)
	if err != nil {
		return err
	}
	etag, err := store.UploadPart(ctx, key, up, 1, data)
	if err == nil {
		err = store.CompleteUpload(ctx, key, up, []blobstore.Part{{Number: 1, ETag: etag}})
	}
	if err != nil {
		_ = store.AbortUpload(context.WithoutCancel(ctx), key, up)
	}
	return err
}
