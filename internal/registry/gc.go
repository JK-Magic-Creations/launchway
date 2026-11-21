package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// GCOptions say how a collection runs.
type GCOptions struct {
	// DryRun reports what a collection would remove and removes nothing.
	DryRun bool
	// Grace overrides the limit's: how old an unreferenced link or blob must
	// be. Zero uses the limit.
	Grace time.Duration
}

// GCReport is what a collection removed, or would remove.
type GCReport struct {
	DryRun    bool     `json:"dry_run"`
	StartedAt int64    `json:"started_at"`
	Uploads   int      `json:"uploads"`
	Links     int      `json:"links"`
	Blobs     int      `json:"blobs"`
	Bytes     int64    `json:"bytes"`
	Strays    int      `json:"strays"`
	Digests   []string `json:"digests,omitempty"` // the first blobs, for a person to look at
	Skipped   string   `json:"skipped,omitempty"`
}

const gcLease = 30 * time.Minute

// GC removes what nothing refers to, in four steps that are safe beside
// pushes:
//
//  1. Uploads idle for longer than the limit.
//  2. A repository's link to a blob that no manifest of the repository needs
//     and that was linked longer ago than the grace period.
//  3. Blobs no repository of the organization links and that no push has
//     touched within the grace period; the row goes first, then the object.
//  4. Objects of the blob store under registry/ that no row names (left by a
//     server that stopped between writing and recording), where the store can
//     list them.
//
// A push always refreshes the age of what it uses inside the transaction
// that records it, and every delete here re-checks the age and the
// references in its own statement, so a blob is never removed from under a
// push that uses it. Only one server collects at a time (a lease in the
// database).
func (s *Service) GC(ctx context.Context, o GCOptions) (GCReport, error) {
	rep := GCReport{DryRun: o.DryRun, StartedAt: s.now()}
	grace := o.Grace
	if grace <= 0 {
		grace = s.limits.GCGrace
	}
	if err := s.ensureStore(); err != nil {
		return rep, err
	}
	if !o.DryRun {
		held, err := s.takeLease(ctx)
		if err != nil {
			return rep, err
		}
		if !held {
			rep.Skipped = "another server is collecting"
			return rep, newErr(http.StatusConflict, CodeDenied, "another server is collecting garbage right now", "Wait for it to finish; collection runs on one server at a time.")
		}
		defer func() {
			_, _ = s.db.Exec(context.WithoutCancel(ctx), `UPDATE registry_gc_state SET lease_until = 0 WHERE id = 1 AND lease_holder = ?`, s.o.Server)
		}()
	}
	cutoff := s.o.Now().Add(-grace).UnixMilli()

	n, err := s.expireUploads(ctx, o.DryRun)
	if err != nil {
		return rep, err
	}
	rep.Uploads = n

	type link struct{ repo, digest string }
	var links []link
	err = scanAll(ctx, s.db, `SELECT l.repo, l.digest FROM registry_repo_blobs l WHERE l.linked_at < ?
		AND NOT EXISTS (SELECT 1 FROM registry_manifest_refs r WHERE r.repo = l.repo AND r.ref = l.digest)
		AND NOT EXISTS (SELECT 1 FROM registry_manifests m WHERE m.repo = l.repo AND m.digest = l.digest)`, []any{cutoff}, func(scan func(...any) error) error {
		var l link
		if err := scan(&l.repo, &l.digest); err != nil {
			return err
		}
		links = append(links, l)
		return nil
	})
	if err != nil {
		return rep, err
	}
	dead := map[link]bool{}
	for _, l := range links {
		if o.DryRun {
			dead[l] = true
			rep.Links++
			continue
		}
		res, err := s.db.Exec(ctx, `DELETE FROM registry_repo_blobs WHERE repo = ? AND digest = ? AND linked_at < ?
			AND NOT EXISTS (SELECT 1 FROM registry_manifest_refs r WHERE r.repo = registry_repo_blobs.repo AND r.ref = registry_repo_blobs.digest)
			AND NOT EXISTS (SELECT 1 FROM registry_manifests m WHERE m.repo = registry_repo_blobs.repo AND m.digest = registry_repo_blobs.digest)`,
			l.repo, l.digest, cutoff)
		if err != nil {
			return rep, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			rep.Links++
		}
	}

	type blob struct {
		org, digest, key string
		size             int64
	}
	var blobs []blob
	err = scanAll(ctx, s.db, `SELECT b.org, b.digest, b.size, b.store_key FROM registry_blobs b WHERE b.touched_at < ? ORDER BY b.org, b.digest`, []any{cutoff}, func(scan func(...any) error) error {
		var b blob
		if err := scan(&b.org, &b.digest, &b.size, &b.key); err != nil {
			return err
		}
		blobs = append(blobs, b)
		return nil
	})
	if err != nil {
		return rep, err
	}
	for _, b := range blobs {
		if o.DryRun {
			holders, err := column(ctx, s.db, `SELECT l.repo FROM registry_repo_blobs l JOIN registry_repositories r ON r.name = l.repo WHERE l.digest = ? AND r.org = ?`, b.digest, b.org)
			if err != nil {
				return rep, err
			}
			live := false
			for _, repo := range holders {
				live = live || !dead[link{repo, b.digest}]
			}
			if live {
				continue
			}
		} else {
			res, err := s.db.Exec(ctx, `DELETE FROM registry_blobs WHERE org = ? AND digest = ? AND touched_at < ?
				AND NOT EXISTS (SELECT 1 FROM registry_repo_blobs l JOIN registry_repositories r ON r.name = l.repo WHERE l.digest = registry_blobs.digest AND r.org = registry_blobs.org)`,
				b.org, b.digest, cutoff)
			if err != nil {
				return rep, err
			}
			if c, _ := res.RowsAffected(); c == 0 {
				continue
			}
			if err := s.blobs.Delete(ctx, b.key); err != nil {
				s.log.Warn("removing a collected blob's object; it is left for the stray sweep", "key", b.key, "err", err)
			}
		}
		rep.Blobs++
		rep.Bytes += b.size
		if len(rep.Digests) < 100 {
			rep.Digests = append(rep.Digests, b.digest)
		}
	}
	sort.Strings(rep.Digests)

	rep.Strays = s.sweepStrays(ctx, o.DryRun, cutoff)

	if !o.DryRun {
		sum, _ := json.Marshal(rep)
		_, _ = s.db.Exec(ctx, `UPDATE registry_gc_state SET last_run_at = ?, last_summary = ? WHERE id = 1`, s.now(), string(sum))
		s.emit(ctx, Event{Type: "gc", Detail: fmt.Sprintf("removed %d blobs (%s), %d links, %d uploads, %d strays", rep.Blobs, human(rep.Bytes), rep.Links, rep.Uploads, rep.Strays), Size: rep.Bytes})
	}
	return rep, nil
}

func (s *Service) takeLease(ctx context.Context) (bool, error) {
	if _, err := s.db.Exec(ctx, `INSERT INTO registry_gc_state(id) VALUES(1) ON CONFLICT (id) DO NOTHING`); err != nil {
		return false, err
	}
	now := s.now()
	res, err := s.db.Exec(ctx, `UPDATE registry_gc_state SET lease_holder = ?, lease_until = ? WHERE id = 1 AND (lease_until < ? OR lease_holder = ?)`,
		s.o.Server, now+gcLease.Milliseconds(), now, s.o.Server)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// LastGC returns the report of the last collection, if there was one.
func (s *Service) LastGC(ctx context.Context) (GCReport, int64, bool, error) {
	var sum string
	var at int64
	err := s.db.QueryRow(ctx, `SELECT last_run_at, last_summary FROM registry_gc_state WHERE id = 1`).Scan(&at, &sum)
	if isNoRows(err) || (err == nil && at == 0) {
		return GCReport{}, 0, false, nil
	}
	if err != nil {
		return GCReport{}, 0, false, err
	}
	var r GCReport
	_ = json.Unmarshal([]byte(sum), &r)
	return r, at, true, nil
}

// keyLister is what the database blob store can do and the others cannot.
type keyLister interface {
	Keys(ctx context.Context, prefix, after string, limit int) ([]string, error)
}

// sweepStrays removes objects under registry/ that no row names, in a store
// that can list them. A filesystem or S3 store cannot, and keeps such objects
// until its own lifecycle rules or an operator remove them (they are rare:
// one needs a server to stop between writing an object and recording it).
func (s *Service) sweepStrays(ctx context.Context, dry bool, cutoff int64) int {
	kl, ok := s.blobs.(keyLister)
	if !ok {
		return 0
	}
	after, removed := "", 0
	for {
		keys, err := kl.Keys(ctx, "registry/", after, 500)
		if err != nil || len(keys) == 0 {
			return removed
		}
		for _, k := range keys {
			after = k
			var one int
			err := s.db.QueryRow(ctx, `SELECT 1 FROM registry_blobs WHERE store_key = ?`, k).Scan(&one)
			if err == nil {
				continue
			}
			if !isNoRows(err) {
				return removed
			}
			if err := s.db.QueryRow(ctx, `SELECT 1 FROM registry_upload_chunks WHERE store_key = ?`, k).Scan(&one); err == nil {
				continue
			}
			var created int64
			if err := s.db.QueryRow(ctx, `SELECT created_at FROM blob_objects WHERE key = ?`, k).Scan(&created); err != nil || created > cutoff {
				continue
			}
			removed++
			if !dry {
				_ = s.blobs.Delete(ctx, k)
			}
		}
		if len(keys) < 500 {
			return removed
		}
	}
}

// Maintain runs retention and garbage collection every interval until ctx
// ends. It is safe on every server of a group: collection takes a lease.
func (s *Service) Maintain(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 6 * time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := s.RunRetention(ctx, "", "", Who{Name: "retention"}); err != nil {
			s.log.Warn("registry retention", "err", err)
		}
		if _, err := s.GC(ctx, GCOptions{}); err != nil && asError(err).Status != http.StatusConflict {
			s.log.Warn("registry garbage collection", "err", err)
		}
	}
}
