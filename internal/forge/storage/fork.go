package storage

// Forks share the packs of their parent by reference.
//
// The choice. A fork could be made by copying every pack of the parent into
// new blobs, or by giving the fork its own manifest rows that name the
// parent's blobs. Copying is the simplest to keep safe (nothing is shared, so
// nothing can be deleted from under anyone) but it takes the parent's whole
// size again for every fork, and a fork is made on a click. Sharing costs one
// row per pack and a rule for when a blob may go, so that is what is built:
//
//   - A pack is immutable and named by its SHA-256; a blob is never changed.
//   - Fork copies, in one transaction on the parent's generation, the parent's
//     branches and tags and one live repo_packs row per live pack of the
//     parent, each naming the parent's blob. Objects pushed to the parent
//     later are in packs the fork has no row for, and no cache of the fork
//     ever downloads them: they are not reachable through the fork.
//   - A blob is deleted only when no row of any repository still needs it
//     (store.BlobInUse: a live pack, a pending one that is not abandoned yet,
//     or a retired one still inside its grace period). Retiring the parent's
//     pack (consolidation) or purging the parent therefore leaves the fork's
//     rows, and so the blob, alone; the same for the other way round.
//   - Each repository's rows are its own: consolidating a fork writes a new
//     pack under the fork's own key and retires only the fork's rows.
//
// What a fork can read is the objects of the packs that were live at the fork:
// the same objects the person forking could already read from the parent,
// including any in those packs that no branch reaches any more (as for the
// parent itself, a client that knows an object's name can ask for it).

import (
	"context"
	"fmt"
	"time"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// ForkResult says what a fork copied.
type ForkResult struct {
	Refs, Packs int
	Generation  int64
}

func (s *Store) begin(ctx context.Context) (store.CtxTx, error) {
	tx, err := s.o.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// Fork makes the child repository (already created, empty) a fork of the
// parent: its refs and its pack manifest are copied in one transaction, so a
// reader sees the child empty or complete, and a crash at any point leaves it
// empty. It does not check who may fork; the caller has done that.
func (s *Store) Fork(ctx context.Context, parent, child string) (ForkResult, error) {
	f := orNone(faultsFrom(ctx))
	if err := f.Hit(PtForkBefore); err != nil {
		return ForkResult{}, err
	}
	r, err := store.ForkLedgerCopy(ctx, s.begin, s.o.DB.Postgres(), parent, child, nowMS(), f.Hit)
	if err != nil {
		return ForkResult{}, err
	}
	if err := f.Hit(PtForkAfter); err != nil {
		return ForkResult{}, err
	}
	return ForkResult{Refs: r.Refs, Packs: r.Packs, Generation: r.Gen}, nil
}

// blobNeeded says whether the blob of a retired or pending pack must stay
// because another repository's pack needs it.
func (s *Store) blobNeeded(ctx context.Context, repo string, p Pack, grace, pendingAge time.Duration) (bool, error) {
	now := nowMS()
	keep, err := store.BlobInUse(ctx, s.o.DB, p.BlobKey, repo, p.ID, now-grace.Milliseconds(), now-pendingAge.Milliseconds())
	if err != nil {
		return true, fmt.Errorf("checking whether another repository needs a pack: %w", err)
	}
	return keep, nil
}
