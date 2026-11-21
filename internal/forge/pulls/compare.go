package pulls

import (
	"context"
	"strings"
	"time"

	"github.com/jk-magic-creations/launchway/internal/gitexec"
)

// Resolve turns a branch name, a tag name or a full commit name into a commit
// of the repository. Anything else is invalid: no revision expression
// (HEAD~3, @{u}, :/text) ever reaches git from a request.
func (s *Service) Resolve(ctx context.Context, repoID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("name a branch or a commit")
	}
	refs, err := s.Git.Refs(ctx, repoID)
	if err != nil {
		return "", err
	}
	for _, pre := range []string{"refs/heads/", "refs/tags/"} {
		if sha, ok := refs[pre+name]; ok {
			return s.commitOf(ctx, repoID, sha)
		}
	}
	if gitexec.ValidSHA(name) && len(name) == 40 {
		return s.commitOf(ctx, repoID, name)
	}
	return "", invalid("%q is not a branch, a tag or a full commit name of this repository", name)
}

func (s *Service) commitOf(ctx context.Context, repoID, sha string) (string, error) {
	var out string
	err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		out, err = g.out(ctx, "rev-parse", "--verify", "--quiet", sha+"^{commit}")
		return err
	})
	if err != nil {
		return "", invalid("%s is not a commit of this repository", sha)
	}
	return out, nil
}

// Diff is what the files tab shows.
type Diff struct {
	Comparison
	MergeBase string `json:"merge_base,omitempty"`
}

// Compare lists the files that changed between two commits of a repository.
func (s *Service) Compare(ctx context.Context, repoID, from, to string, lim Limits) (Comparison, error) {
	var c Comparison
	err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		c, err = g.compare(ctx, from, to, lim)
		return err
	})
	return c, err
}

// Files lists the files a pull request changes: the difference between the
// merge base (where the branches diverged) and the head, so changes that
// landed on the target since are not shown as the pull request's own.
func (s *Service) Files(ctx context.Context, repoID string, p PullRequest, lim Limits) (Diff, error) {
	from := p.MergeBase
	if from == "" {
		from = p.BaseSHA
	}
	c, err := s.Compare(ctx, repoID, from, p.HeadSHA, lim)
	return Diff{Comparison: c, MergeBase: p.MergeBase}, err
}

// PatchOf returns the patch between two commits: of the whole comparison, or
// of the given paths. A patch over the default limit is held back
// (Held, with Binary when the file is binary) unless full is set; the hard
// ceiling holds either way and cuts the patch (Truncated).
func (s *Service) PatchOf(ctx context.Context, repoID, from, to string, paths []string, full bool, lim Limits) (Patch, error) {
	var out Patch
	err := s.read(ctx, repoID, func(g gitRunner) error {
		max := lim.MaxDiffBytes
		if len(paths) > 0 {
			max = lim.MaxPatchBytes
			// A file whose line counts are over the limit is held back before it is read.
			if !full {
				c, err := g.compare(ctx, from, to, lim)
				if err != nil {
					return err
				}
				for _, f := range c.Files {
					for _, p := range paths {
						if (f.Path == p || f.OldPath == p) && f.Binary {
							out = Patch{Path: f.Path, Binary: true, Diff: "", Bytes: 0}
							return nil
						}
						if (f.Path == p || f.OldPath == p) && f.TooLarge {
							out = Patch{Path: f.Path, Held: true}
							return nil
						}
					}
				}
			}
		}
		if full {
			max = lim.HardBytes
		}
		var err error
		out, err = g.patch(ctx, from, to, paths, max)
		return err
	})
	return out, err
}

// Commits lists the commits of a pull request, oldest first.
func (s *Service) Commits(ctx context.Context, repoID string, p PullRequest) ([]CommitInfo, error) {
	var out []CommitInfo
	err := s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		out, err = g.commits(ctx, p.BaseSHA, p.HeadSHA, 1000)
		if p.State == Merged && p.MergeMethod != "manual" {
			// After a merge the target holds them: list from the merge base.
			out, err = g.commits(ctx, p.MergeBase, p.HeadSHA, 1000)
		}
		return err
	})
	return out, err
}

// Since is "changes since your last review": the files that changed between
// the head a revision recorded and the head now.
type Since struct {
	Comparison
	FromRevision int `json:"from_revision"`
	// Rebased says the base moved between the two heads, so the comparison
	// also shows what came in from the target.
	Rebased bool `json:"rebased,omitempty"`
}

// ChangesSince compares the head of a revision with the current head.
func (s *Service) ChangesSince(ctx context.Context, repoID string, p PullRequest, seq int, lim Limits) (Since, error) {
	revs, err := s.Store.Revisions(ctx, p.ID)
	if err != nil {
		return Since{}, err
	}
	var from *Revision
	for i := range revs {
		if revs[i].Seq == seq {
			from = &revs[i]
		}
	}
	if from == nil {
		return Since{}, invalid("this pull request has no revision %d; it has %d", seq, len(revs))
	}
	c, err := s.Compare(ctx, repoID, from.HeadSHA, p.HeadSHA, lim)
	return Since{Comparison: c, FromRevision: seq, Rebased: from.BaseSHA != p.BaseSHA}, err
}

// BranchComparison is the compare page: what a pull request from head to
// base would contain, before it exists.
type BranchComparison struct {
	Base      string       `json:"base"`
	Head      string       `json:"head"`
	BaseSHA   string       `json:"base_sha"`
	HeadSHA   string       `json:"head_sha"`
	MergeBase string       `json:"merge_base,omitempty"`
	Ahead     int          `json:"ahead"`
	Behind    int          `json:"behind"`
	Commits   []CommitInfo `json:"commits"`
	Files     Comparison   `json:"changes"`
	Mergeable Mergeability `json:"mergeability"`
	// Existing is the number of an open pull request between the same two
	// branches (0: none).
	Existing int `json:"existing_pull,omitempty"`
}

// CompareBranches compares two branches of a repository.
func (s *Service) CompareBranches(ctx context.Context, repoID, base, head string, lim Limits) (BranchComparison, error) {
	bc := BranchComparison{Base: base, Head: head, Commits: []CommitInfo{}}
	var err error
	if bc.BaseSHA, err = s.Resolve(ctx, repoID, base); err != nil {
		return bc, err
	}
	if bc.HeadSHA, err = s.Resolve(ctx, repoID, head); err != nil {
		return bc, err
	}
	err = s.read(ctx, repoID, func(g gitRunner) error {
		var err error
		if bc.MergeBase, err = g.mergeBase(ctx, bc.BaseSHA, bc.HeadSHA); err != nil {
			return err
		}
		if bc.Behind, bc.Ahead, err = g.counts(ctx, bc.BaseSHA, bc.HeadSHA); err != nil {
			return err
		}
		if bc.Commits, err = g.commits(ctx, bc.BaseSHA, bc.HeadSHA, 1000); err != nil {
			return err
		}
		from := bc.MergeBase
		if from == "" {
			from = bc.BaseSHA
		}
		bc.Files, err = g.compare(ctx, from, bc.HeadSHA, lim)
		return err
	})
	if err != nil {
		return bc, err
	}
	// The judgement is bounded: a page that asks for it never waits long for a slow merge.
	jctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	m, err := s.judgement(jctx, repoID, PullRequest{BaseSHA: bc.BaseSHA, HeadSHA: bc.HeadSHA})
	if err != nil {
		m = Mergeability{State: MergeUnknown, Reason: err.Error()}
	}
	bc.Mergeable = m
	if open, _ := s.Store.OpenFromSource(ctx, repoID, head); len(open) > 0 {
		for _, o := range open {
			if o.TargetBranch == base {
				bc.Existing = o.Number
			}
		}
	}
	return bc, nil
}
