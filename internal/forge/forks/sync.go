package forks

import (
	"context"
	"fmt"
	"strings"

	"github.com/jk-magic-creations/launchway/internal/forge"
	"github.com/jk-magic-creations/launchway/internal/forge/storage"
)

// Sync statuses.
const (
	StatusUpToDate      = "up_to_date"
	StatusFastForwarded = "fast_forwarded"
	StatusMerged        = "merged"
	StatusCreated       = "created"
	// StatusDiverged: the fork's branch has commits of its own and the mode was
	// fast-forward; StatusConflicts: it was merge and the merge conflicts.
	StatusDiverged  = "diverged"
	StatusConflicts = "conflicts"
)

// SyncResult is what keeping a fork current did, or why it did not.
type SyncResult struct {
	Status    string   `json:"status"`
	Branch    string   `json:"branch"`
	Upstream  string   `json:"upstream"`
	Head      string   `json:"head,omitempty"`
	Ahead     int      `json:"ahead"`
	Behind    int      `json:"behind"`
	Conflicts []string `json:"conflicting_files,omitempty"`
}

// OK reports whether the fork's branch is now current (or already was).
func (r SyncResult) OK() bool {
	switch r.Status {
	case StatusUpToDate, StatusFastForwarded, StatusMerged, StatusCreated:
		return true
	}
	return false
}

// Standing is how far a fork's branch is from its parent's: the commits it has
// that the parent lacks (ahead) and the parent has that it lacks (behind).
type Standing struct {
	Branch   string `json:"branch"`
	Upstream string `json:"upstream"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	// Missing says the fork has no such branch.
	Missing bool `json:"missing,omitempty"`
}

// related finds a fork's parent and refuses what the relationship and the
// visibility rule do not allow.
func (s *Service) related(ctx context.Context, fork forge.Repository) (forge.Repository, error) {
	fi, err := s.Info(ctx, fork.ID)
	if err != nil {
		return forge.Repository{}, err
	}
	parent, err := s.Parent(ctx, fi)
	if err != nil {
		return forge.Repository{}, err
	}
	if err := SyncAllowed(parent.Visibility, fork.Visibility); err != nil {
		return forge.Repository{}, err
	}
	return parent, nil
}

// Standing reads how far a fork's branch is from the parent's branch of the
// same name (upstream "" means the fork's default branch on both sides).
func (s *Service) Standing(ctx context.Context, fork forge.Repository, branch, upstream string) (Standing, error) {
	if branch == "" {
		branch = fork.DefaultBranch
	}
	if upstream == "" {
		upstream = branch
	}
	st := Standing{Branch: branch, Upstream: upstream}
	parent, err := s.related(ctx, fork)
	if err != nil {
		return st, err
	}
	refs, err := s.Git.Refs(ctx, fork.ID)
	if err != nil {
		return st, err
	}
	if refs[branchRef(branch)] == "" {
		st.Missing = true
		return st, nil
	}
	_, err = s.Git.Transfer(ctx, fork.ID, parent.ID, []string{branchRef(upstream)}, func(ctx context.Context, scratch string, dst, tips map[string]string) ([]storage.RefUpdate, error) {
		g := runner{dir: scratch, limits: s.Git.Limits()}
		var err error
		st.Ahead, st.Behind, err = g.counts(ctx, tips[branchRef(upstream)], dst[branchRef(branch)])
		return nil, err
	})
	return st, err
}

// Sync brings the parent's branch into the fork's: a fast-forward when the
// fork's branch has nothing of its own, otherwise (mode "merge") a merge commit
// by the person, or the answer that it conflicts. Mode "fast-forward" (the
// default) leaves a diverged branch alone and says so. Only the objects of the
// parent's branch come over, never its other branches or its pull requests.
func (s *Service) Sync(ctx context.Context, fork forge.Repository, branch, upstream, mode, by string) (SyncResult, error) {
	if branch == "" {
		branch = fork.DefaultBranch
	}
	if upstream == "" {
		upstream = branch
	}
	switch mode {
	case "", "fast-forward", "merge":
	default:
		return SyncResult{}, fmt.Errorf("%w: sync by fast-forward or merge", forge.ErrInvalid)
	}
	res := SyncResult{Branch: branch, Upstream: upstream}
	parent, err := s.related(ctx, fork)
	if err != nil {
		return res, err
	}
	fref, uref := branchRef(branch), branchRef(upstream)
	var made string
	_, err = s.Git.Transfer(ctx, fork.ID, parent.ID, []string{uref}, func(ctx context.Context, scratch string, dst, tips map[string]string) ([]storage.RefUpdate, error) {
		up, head := tips[uref], dst[fref]
		res.Head = head
		if head == "" {
			res.Status, res.Head = StatusCreated, up
			return []storage.RefUpdate{{Name: fref, New: up}}, nil
		}
		g := s.runner(scratch, ident(by, s.now()))
		var err error
		if res.Ahead, res.Behind, err = g.counts(ctx, up, head); err != nil {
			return nil, err
		}
		if res.Behind == 0 {
			res.Status = StatusUpToDate
			return nil, nil
		}
		if res.Ahead == 0 {
			res.Status, res.Head = StatusFastForwarded, up
			return []storage.RefUpdate{{Name: fref, Old: head, New: up}}, nil
		}
		if mode != "merge" {
			res.Status = StatusDiverged
			return nil, nil
		}
		tree, files, clean, err := g.trial(ctx, head, up)
		if err != nil {
			return nil, err
		}
		if !clean {
			res.Status, res.Conflicts = StatusConflicts, files
			return nil, nil
		}
		made, err = g.out(ctx, "", "commit-tree", tree, "-p", head, "-p", up, "-m", fmt.Sprintf("Merge branch '%s' of %s into %s", upstream, parent.Path, branch))
		if err != nil {
			return nil, err
		}
		res.Status, res.Head = StatusMerged, made
		return []storage.RefUpdate{{Name: fref, Old: head, New: made}}, nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// Moved says whether a sync changed the fork's branch (the pull requests and
// pipelines of the fork must be told).
func (r SyncResult) Moved() bool {
	return r.Status == StatusFastForwarded || r.Status == StatusMerged || r.Status == StatusCreated
}

// Describe is a sentence for a person.
func (r SyncResult) Describe() string {
	switch r.Status {
	case StatusUpToDate:
		return fmt.Sprintf("%s is already up to date with the parent's %s", r.Branch, r.Upstream)
	case StatusFastForwarded:
		return fmt.Sprintf("%s moved forward to the parent's %s (%d new commits)", r.Branch, r.Upstream, r.Behind)
	case StatusMerged:
		return fmt.Sprintf("the parent's %s was merged into %s", r.Upstream, r.Branch)
	case StatusCreated:
		return fmt.Sprintf("%s was made from the parent's %s", r.Branch, r.Upstream)
	case StatusDiverged:
		return fmt.Sprintf("%s has %d commits the parent lacks and the parent has %d it lacks; merge instead, or discard your commits", r.Branch, r.Ahead, r.Behind)
	case StatusConflicts:
		return fmt.Sprintf("merging the parent's %s into %s conflicts in %s; resolve them on your computer", r.Upstream, r.Branch, strings.Join(r.Conflicts, ", "))
	}
	return r.Status
}
