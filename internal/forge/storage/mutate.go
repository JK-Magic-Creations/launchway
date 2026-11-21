package storage

import (
	"context"
	"os"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// MutateFunc makes new objects (commits, trees) and says which refs move. It
// is given the refs of record as they were when the mutation began, and a
// scratch bare repository that borrows the repository's objects: whatever
// objects it writes there are stored as one pack if the update commits. The
// updates it returns carry Old values taken from refs, so the commit is a
// compare-and-swap: when a push (or another mutation) moved any of those refs
// in the meantime, the whole mutation fails with *ErrStale and changes nothing.
// The function may run more than once when the caller retries; it must not
// have effects outside the scratch repository.
type MutateFunc func(ctx context.Context, refs map[string]string, scratch string) ([]RefUpdate, error)

// Limits are the ceilings applied to every git process of this store, for
// callers that run git in the scratch repository.
func (s *Store) Limits() gitexec.Limits { return s.o.Limits }

// Mutate is the server-side write path: a merge, a revert, a suggestion
// applied as a commit, or a plain move of a ref that is not a push. It does
// not check who may write; the caller (the pull request service) has done
// that. Like a push it ends in the ledger's one transaction, so readers see
// the repository before or after, never between, and a concurrent push is
// never lost.
func (s *Store) Mutate(ctx context.Context, repo string, fn MutateFunc) (ConvergeResult, error) {
	var res ConvergeResult
	if err := s.ensureCurrent(ctx, repo); err != nil {
		return res, err
	}
	g, err := s.guardFor(repo)
	if err != nil {
		return res, err
	}
	defer g.release()
	if err := g.shared(); err != nil {
		return res, err
	}
	snap, err := s.l.Snapshot(ctx, repo)
	if err != nil {
		return res, err
	}
	scratch, err := os.MkdirTemp(s.o.CacheDir, ".mutate-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(scratch)
	if err := s.prepareScratch(ctx, scratch, s.cacheDir(repo), snap.Refs); err != nil {
		return res, err
	}
	refs := make(map[string]string, len(snap.Refs))
	for k, v := range snap.Refs {
		refs[k] = v
	}
	ups, err := fn(ctx, refs, scratch)
	if err != nil {
		return res, err
	}
	res.Generation = snap.Gen
	var moved []RefUpdate
	for _, u := range ups {
		if norm(u.Old) == norm(u.New) {
			res.Unchanged++
			continue
		}
		switch {
		case isZero(u.Old):
			res.Created++
		case isZero(u.New):
			res.Deleted++
		default:
			res.Updated++
		}
		moved = append(moved, u)
	}
	if len(moved) == 0 {
		return res, nil
	}
	err = s.storeAndCommit(ctx, repo, scratch, snap.Refs, moved, &res)
	return res, err
}

// Scratch runs fn with a scratch bare repository that borrows the repository's
// objects, for reading that needs to write objects it will throw away: a trial
// merge (git merge-tree --write-tree) writes the merged trees. Nothing is
// committed and the repository is not changed.
func (s *Store) Scratch(ctx context.Context, repo string, fn func(ctx context.Context, refs map[string]string, scratch string) error) error {
	_, err := s.Mutate(ctx, repo, func(ctx context.Context, refs map[string]string, scratch string) ([]RefUpdate, error) {
		return nil, fn(ctx, refs, scratch)
	})
	return err
}
