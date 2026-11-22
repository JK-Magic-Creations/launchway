package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// The named points of a permanent removal where a failure can be injected, in
// order. A test stops the removal at each of them and runs it again.
const (
	PtPurgeStart       = "purge:start"
	PtPurgeAfterFlag   = "purge:after-flag"
	PtPurgeAfterRefs   = "purge:after-refs"
	PtPurgeBeforeBlob  = "purge:before-blob"
	PtPurgeAfterBlob   = "purge:after-blob"
	PtPurgeAfterRow    = "purge:after-pack-row"
	PtPurgeBeforeFinal = "purge:before-ledger"
	PtPurgeAfterLedger = "purge:after-ledger"
)

// PurgePoints is every point of a removal, in order.
var PurgePoints = []string{PtPurgeStart, PtPurgeAfterFlag, PtPurgeAfterRefs, PtPurgeBeforeBlob, PtPurgeAfterBlob, PtPurgeAfterRow,
	PtPurgeBeforeFinal, PtPurgeAfterLedger}

// ErrPurgeBusy means another server holds the repository's lease: it is being
// repacked or removed there. Try again later.
var ErrPurgeBusy = errors.New("the repository is being worked on by another server")

// PurgeResult is what a removal did.
type PurgeResult struct {
	// Packs is how many pack rows were removed; BlobsDeleted how many blobs
	// went with them; BlobsKept how many were left because a pack of another
	// repository still names them.
	Packs, BlobsDeleted, BlobsKept int
	// Bytes is what the deleted blobs took.
	Bytes int64
}

// flagPurging is the reason a repository's storage carries while it is being
// removed: maintenance and verification leave a flagged repository alone.
const flagPurging = "being removed for good"

// Purge removes a repository's Git storage for good: its refs, its pack rows
// and the pack blobs that no other repository's manifest names, and last the
// ledger rows that make the repository known. It may be stopped at any point
// (a crash, a lost connection, a killed server) and run again: every step can
// repeat, and the repository stays known to the ledger until the last step, so
// an unfinished removal is always found and finished. The order is:
//
//  1. take the repository's lease and flag it, so maintenance leaves it alone;
//  2. delete its refs, so nothing points at objects any more;
//  3. for each pack row, delete the blob (unless a pack of another
//     repository names the same blob), then the row: a row is never removed
//     before its blob, so a blob is never left that nothing names;
//  4. delete the generation and lease rows (the ledger forgets the repository),
//     and sweep once more for a pack a push in flight registered meanwhile;
//  5. delete this server's cache. The caches of other servers are disposable
//     and age out under their size limit.
//
// A blob whose upload never reached the manifest (a push that died before it
// registered its pack) is not named by anything and the blob store cannot list
// keys; it is the same leftover maintenance cannot collect, and it is small and
// rare. A repository the ledger does not know is already removed: nothing to do.
func (s *Store) Purge(ctx context.Context, repo string) (PurgeResult, error) {
	var res PurgeResult
	f := orNone(faultsFrom(ctx))
	if !repoRE.MatchString(repo) {
		return res, fmt.Errorf("invalid repository id %q", repo)
	}
	if err := f.Hit(PtPurgeStart); err != nil {
		return res, err
	}
	// The lease is the one maintenance takes, held for the removal's own
	// holder name so that a restarted removal on this server takes it again.
	ok, err := s.l.Lease(ctx, repo, "purge:"+s.o.Server, 10*time.Minute)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, ErrPurgeBusy
	}
	db := s.o.DB
	known, err := s.l.Exists(ctx, repo)
	if err != nil {
		return res, err
	}
	if known {
		if err := store.FlagRepoStorage(ctx, db, repo, flagPurging); err != nil {
			return res, err
		}
	}
	if err := f.Hit(PtPurgeAfterFlag); err != nil {
		return res, err
	}
	if err := store.DeleteRefRows(ctx, db, repo); err != nil {
		return res, err
	}
	if err := f.Hit(PtPurgeAfterRefs); err != nil {
		return res, err
	}
	if err := s.purgePacks(ctx, repo, f, &res); err != nil {
		return res, err
	}
	if err := f.Hit(PtPurgeBeforeFinal); err != nil {
		return res, err
	}
	if err := store.DeleteRepoLedger(ctx, db, repo); err != nil {
		return res, err
	}
	if err := f.Hit(PtPurgeAfterLedger); err != nil {
		return res, err
	}
	// A push in flight when the removal began may have registered a pack
	// after the first pass; the generation is gone now, so no more can be
	// made live, and this pass removes what is left.
	if err := s.purgePacks(ctx, repo, f, &res); err != nil {
		return res, err
	}
	return res, s.Wipe(repo)
}

func (s *Store) purgePacks(ctx context.Context, repo string, f Faults, res *PurgeResult) error {
	db := s.o.DB
	packs, err := store.PacksOf(ctx, db, repo)
	if err != nil {
		return err
	}
	for _, p := range packs {
		if err := f.Hit(PtPurgeBeforeBlob); err != nil {
			return err
		}
		shared, err := store.BlobUsedElsewhere(ctx, db, repo, p.BlobKey)
		if err != nil {
			return err
		}
		if shared {
			res.BlobsKept++
		} else {
			// Deleting a blob that is already gone is not an error.
			if err := s.o.Blobs.Delete(ctx, p.BlobKey); err != nil {
				return fmt.Errorf("deleting the blob of pack %s: %w", short(p.ID), err)
			}
			res.BlobsDeleted++
			res.Bytes += p.Bytes
		}
		if err := f.Hit(PtPurgeAfterBlob); err != nil {
			return err
		}
		if err := store.DeletePackRow(ctx, db, repo, p.ID); err != nil {
			return err
		}
		res.Packs++
		if err := f.Hit(PtPurgeAfterRow); err != nil {
			return err
		}
	}
	return nil
}
