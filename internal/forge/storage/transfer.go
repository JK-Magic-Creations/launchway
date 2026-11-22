package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// PlanFunc decides, inside a transfer between two repositories, which refs of
// the destination move. dstRefs are the destination's refs of record, srcTips
// the commits of the source's refs that were asked for (by name), and scratch
// a bare repository in which both repositories' objects can be read (git
// there must be run with AllowAlternates). It returns the updates; their Old
// values should come from dstRefs so the commit is a compare-and-swap.
type PlanFunc func(ctx context.Context, scratch string, dstRefs, srcTips map[string]string) ([]RefUpdate, error)

// Transfer brings objects from one hosted repository into another, in the
// destination's one commit: the objects that the new tips of the destination's
// refs need and the destination lacks are stored as a pack of the destination,
// and the refs move in the ledger's transaction. It is how a pull request from
// a fork gets its head into the target repository, how a fork takes changes
// from its parent, and how the base is brought into a fork's branch.
//
// Nothing is fetched over a transport and no shell runs: the source's objects
// are made readable in the scratch repository as a second alternate, and the
// pack is built from the tips the plan names. Only the objects reachable from
// those tips (and not already in the destination) are copied; nothing else of
// the source's, its other branches included, comes with them.
//
// The caller checked who may read the source and who may write the
// destination. srcRefs names the source refs the plan wants the tips of.
func (s *Store) Transfer(ctx context.Context, dst, src string, srcRefs []string, plan PlanFunc) (ConvergeResult, error) {
	var res ConvergeResult
	err := s.WithCache(ctx, src, func(srcDir string) error {
		tips := map[string]string{}
		snap, err := s.l.Snapshot(ctx, src)
		if err != nil {
			return err
		}
		for _, n := range srcRefs {
			sha, ok := snap.Refs[n]
			if !ok {
				return fmt.Errorf("%w: %s has no %s", ErrNoSuchRef, src, n)
			}
			tips[n] = sha
		}
		res, err = s.Mutate(ctx, dst, func(ctx context.Context, refs map[string]string, scratch string) ([]RefUpdate, error) {
			if err := addAlternate(scratch, filepath.Join(srcDir, "objects")); err != nil {
				return nil, err
			}
			return plan(ctx, scratch, refs, tips)
		})
		return err
	})
	return res, err
}

// ErrNoSuchRef is returned for a ref a repository does not have.
var ErrNoSuchRef = fmt.Errorf("no such ref")

// addAlternate lets the scratch repository read another object directory.
func addAlternate(scratch, objects string) error {
	f, err := os.OpenFile(filepath.Join(scratch, "objects", "info", "alternates"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(objects + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Scratch runner for plans: git in a scratch repository that borrows objects.
func (s *Store) ScratchGit(ctx context.Context, scratch, stdin string, args ...string) (string, error) {
	return s.scratchGit(ctx, scratch, stdin, args...)
}
