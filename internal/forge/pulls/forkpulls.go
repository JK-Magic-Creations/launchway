package pulls

// Pull requests from a fork (docs/guide/forks.md).
//
// The head of such a pull request lives in the fork. Opening it brings the head
// commit, and only the objects that commit needs which the target repository
// lacks, into the target repository, under its pull request ref
// (refs/pull/N/head), through storage.Transfer: no transport, no shell, and
// none of the fork's other branches. From then on the pull request is, for
// every purpose (diff, compare, judgment, merge, pipelines), an ordinary one
// whose head is that ref. When the fork's branch moves, the new head is brought
// over the same way; the pull request ref is the only door the fork has into
// the target repository.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge/storage"
	"github.com/jk-magic-creations/launchway/internal/store"
)

// headRef is the ref, in the repository the pull request is in, that holds its
// head: the source branch, or for a pull request from a fork its pull ref.
func (p PullRequest) headRef() string {
	if p.SourceRepo != "" {
		return PullRef(p.Number)
	}
	return branchRef(p.SourceBranch)
}

// openFromFork opens a pull request whose source branch is in the fork
// n.SourceRepo. The caller checked that the author may read the fork and the
// target, and may publish the fork's branch into the target.
func (s *Service) openFromFork(ctx context.Context, repoID, author string, n New) (PullRequest, error) {
	if s.SameNetwork == nil {
		return PullRequest{}, invalid("pull requests from forks are not available on this server")
	}
	if n.SourceRepo == repoID {
		return PullRequest{}, invalid("the head repository is this repository; leave it out to use one of its own branches")
	}
	if ok, err := s.SameNetwork(ctx, n.SourceRepo, repoID); err != nil {
		return PullRequest{}, err
	} else if !ok {
		return PullRequest{}, invalid("a pull request can come only from a fork of this repository, from its parent, or from another fork of the same repository")
	}
	srefs, err := s.Git.Refs(ctx, n.SourceRepo)
	if err != nil {
		return PullRequest{}, err
	}
	trefs, err := s.Git.Refs(ctx, repoID)
	if err != nil {
		return PullRequest{}, err
	}
	switch {
	case srefs[branchRef(n.SourceBranch)] == "":
		return PullRequest{}, invalid("the branch %s does not exist in the fork; push it first", n.SourceBranch)
	case trefs[branchRef(n.TargetBranch)] == "":
		return PullRequest{}, invalid("the branch %s does not exist; choose a branch of this repository to merge into", n.TargetBranch)
	}
	if open, err := store.OpenPullsFromFork(ctx, s.Store.db, n.SourceRepo, n.SourceBranch); err == nil {
		for _, o := range open {
			if o.RepoID != repoID {
				continue
			}
			if d, err := s.Store.Get(ctx, repoID, o.Number); err == nil && d.TargetBranch == n.TargetBranch {
				return PullRequest{}, conflict("pull request #%d already goes from that branch of the fork to %s", d.Number, n.TargetBranch)
			}
		}
	}
	num, err := s.Store.nextNumber(ctx, repoID)
	if err != nil {
		return PullRequest{}, err
	}
	var head, base, mb string
	_, err = s.Git.Transfer(ctx, repoID, n.SourceRepo, []string{branchRef(n.SourceBranch)}, func(ctx context.Context, scratch string, dst, tips map[string]string) ([]storage.RefUpdate, error) {
		head, base = tips[branchRef(n.SourceBranch)], dst[branchRef(n.TargetBranch)]
		if base == "" {
			return nil, invalid("the branch %s does not exist; choose a branch of this repository to merge into", n.TargetBranch)
		}
		g := s.runner(scratch, true)
		contained, err := g.isAncestor(ctx, head, base)
		if err != nil {
			return nil, err
		}
		if contained {
			return nil, invalid("the fork's %s has no commits that %s lacks; there is nothing to merge", n.SourceBranch, n.TargetBranch)
		}
		if mb, err = g.mergeBase(ctx, base, head); err != nil {
			return nil, err
		}
		return []storage.RefUpdate{{Name: PullRef(num), New: head}, {Name: RevisionRef(num, 1), New: head}}, nil
	})
	if err != nil {
		return PullRequest{}, err
	}
	n.Number = num
	p, err := s.Store.Create(ctx, repoID, author, n, head, base, mb)
	if err != nil {
		_ = s.dropRefs(ctx, repoID, PullRef(num), RevisionRef(num, 1))
		return PullRequest{}, err
	}
	s.schedule(ctx, repoID, p.ID)
	s.emit(ctx, "opened", repoID, p, author, nil)
	if !p.Draft {
		s.AutoAssign(ctx, repoID, p, "system")
	}
	return p, nil
}

// dropRefs deletes refs of a repository, for what a failed step leaves behind.
func (s *Service) dropRefs(ctx context.Context, repoID string, names ...string) error {
	_, err := s.Git.Mutate(context.WithoutCancel(ctx), repoID, func(_ context.Context, refs map[string]string, _ string) ([]storage.RefUpdate, error) {
		var ups []storage.RefUpdate
		for _, n := range names {
			if refs[n] != "" {
				ups = append(ups, storage.RefUpdate{Name: n, Old: refs[n], New: strings.Repeat("0", 40)})
			}
		}
		return ups, nil
	})
	return err
}

// forkHeadsMoved is told of a push to a repository: every open pull request in
// another repository that has one of the moved branches as its head is given
// the new head.
func (s *Service) forkHeadsMoved(ctx context.Context, forkID, actor string, ups []RefUpdate) {
	for _, u := range ups {
		br, ok := strings.CutPrefix(u.Name, "refs/heads/")
		if !ok || zero(u.New) {
			continue
		}
		open, err := store.OpenPullsFromFork(ctx, s.Store.db, forkID, br)
		if err != nil {
			s.Log.Warn("looking for pull requests from a fork", "err", err)
			continue
		}
		for _, o := range open {
			p, err := s.Store.Get(ctx, o.RepoID, o.Number)
			if err != nil {
				continue
			}
			if err := s.bringHead(ctx, p, u.New); err != nil {
				s.Log.Warn("a fork's branch moved and its pull request could not take the new head", "pull", p.Number, "err", err)
				continue
			}
			s.headMoved(ctx, p.RepoID, p, u.New, actor)
		}
	}
}

// bringHead moves the pull ref of a pull request from a fork to a new head of
// the fork's source branch, bringing its objects with it. head must be the tip
// of that branch of the fork as the ledger has it.
func (s *Service) bringHead(ctx context.Context, p PullRequest, head string) error {
	srefs, err := s.Git.Refs(ctx, p.SourceRepo)
	if err != nil {
		return err
	}
	if srefs[branchRef(p.SourceBranch)] != head {
		return conflict("the fork's branch %s moved again; the pull request will follow", p.SourceBranch)
	}
	for range 4 {
		_, err = s.Git.Transfer(ctx, p.RepoID, p.SourceRepo, []string{branchRef(p.SourceBranch)}, func(_ context.Context, _ string, dst, tips map[string]string) ([]storage.RefUpdate, error) {
			return []storage.RefUpdate{{Name: PullRef(p.Number), Old: dst[PullRef(p.Number)], New: tips[branchRef(p.SourceBranch)]}}, nil
		})
		var stale *storage.ErrStale
		if !asStale(err, &stale) {
			return err
		}
	}
	return err
}

func asStale(err error, target **storage.ErrStale) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*storage.ErrStale) //nolint:errorlint // ErrStale is returned unwrapped by the ledger
	if ok {
		*target = e
	}
	return ok
}

// SetMaintainerEdits turns on or off the pull request's allowance for the
// target repository's maintainers to update its branch in the fork.
func (s *Service) SetMaintainerEdits(ctx context.Context, p PullRequest, on bool) error {
	if p.SourceRepo == "" {
		return invalid("maintainer edits are for pull requests from a fork; a branch of this repository is edited by whoever may push to it")
	}
	return store.SetPullEdits(ctx, s.Store.db, p.RepoID, p.Number, on)
}

// ApproveRun records that a person with write access to the target approved the
// pipeline of this pull request's current head to run.
func (s *Service) ApproveRun(ctx context.Context, p PullRequest) error {
	if p.SourceRepo == "" {
		return invalid("only a pull request from a fork waits for approval to run")
	}
	return store.ApproveRunHead(ctx, s.Store.db, p.RepoID, p.Number, p.HeadSHA)
}

// ForkRunNeedsApproval says whether the pipeline of this pull request's head
// may not start yet: it is from a fork and its author has no pull request
// merged into the target, and nobody approved this head.
func (s *Service) ForkRunNeedsApproval(ctx context.Context, repoID string, number int) (needs bool, why string, err error) {
	p, err := store.ForkPullOf(ctx, s.Store.db, repoID, number)
	if err != nil || p.SourceRepo == "" {
		return false, "", err
	}
	if p.ApprovedHead == p.HeadSHA && p.HeadSHA != "" {
		return false, "", nil
	}
	merged, err := store.AuthorHasMergedPull(ctx, s.Store.db, repoID, p.Author)
	if err != nil {
		return false, "", err
	}
	if merged {
		return false, "", nil
	}
	return true, fmt.Sprintf("pull request #%d is from a fork and its author %s has no pull request merged here yet: a person with write access must approve running its pipeline", number, p.Author), nil
}

// IsFork reports whether a pull request of the repository comes from a fork.
func (s *Service) IsFork(ctx context.Context, repoID string, number int) (bool, error) {
	p, err := store.ForkPullOf(ctx, s.Store.db, repoID, number)
	if err != nil {
		return false, err
	}
	return p.SourceRepo != "", nil
}
