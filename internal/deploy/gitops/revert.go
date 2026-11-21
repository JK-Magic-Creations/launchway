package gitops

import (
	"context"
	"errors"
	"fmt"
)

// Revert reverts commit on the fresh tip of the branch, unless the branch's
// recent history already has a commit with the trailer
// "Launchway-Revert: <deployment>". The revert commit carries both
// "Launchway-Deployment: <deployment>" and "Launchway-Revert: <deployment>"
// trailers and the given message (subject and body; git's usual
// "This reverts commit <sha>." line is added to the body). A rejected push
// retries on the new tip like Apply. Commit must be an ancestor of the
// branch tip; otherwise it is an error saying so (never revert something
// that is not on the branch).
func (r *Repo) Revert(ctx context.Context, deployment, commit, message string) (RevertResult, error) {
	res, err := r.RevertSeries(ctx, []RevertItem{{Deployment: deployment, Commit: commit, Message: message}})
	if err != nil {
		return RevertResult{}, err
	}
	return RevertResult{Commit: res.Commits[0], Existing: res.Existing, OnWork: res.OnWork}, nil
}

// RevertItem is one deployment's commit to revert in a series.
type RevertItem struct {
	Deployment string // the deployment whose commit is reverted; its trailers go on the revert
	Commit     string
	Message    string // subject and body of the revert commit
}

// SeriesResult is what RevertSeries did.
type SeriesResult struct {
	// Commits are the revert commits, one per item in the order given: made
	// now, or found by their trailer.
	Commits []string
	// Existing: every item was found already; nothing was pushed.
	Existing bool
	// OnWork says the revert is on the work branch, not yet on the branch.
	OnWork bool
}

// RevertSeries reverts several commits, in the order given (newest change
// first, so each revert applies to the state its commit left), one revert
// commit each, and pushes them together. It is all or nothing: if one revert
// does not apply (later commits changed the same lines) it returns a
// *ConflictError naming that item's deployment and the files, and nothing is
// pushed. An item whose "Launchway-Revert: <deployment>" trailer is already
// in the branch's recent history is skipped, so the series can be retried
// from any machine, and a push rejected because the branch moved starts
// again on the new tip like Apply.
func (r *Repo) RevertSeries(ctx context.Context, items []RevertItem) (SeriesResult, error) {
	if err := r.check(); err != nil {
		return SeriesResult{}, err
	}
	if len(items) == 0 {
		return SeriesResult{}, errors.New("the series names nothing to revert")
	}
	for _, it := range items {
		if err := checkName("deployment", it.Deployment); err != nil {
			return SeriesResult{}, err
		}
		if err := checkRevision(it.Commit); err != nil {
			return SeriesResult{}, err
		}
	}
	if err := r.checkAuthor(); err != nil {
		return SeriesResult{}, err
	}
	tries := r.tries()
	for try := 1; ; try++ {
		tip, err := r.refresh(ctx)
		if err != nil {
			return SeriesResult{}, err
		}
		if len(items) == 1 {
			if w, err := r.existingWork(ctx, revertKey+items[0].Deployment); err != nil {
				return SeriesResult{}, err
			} else if w != "" {
				r.logf("The revert of %s is already on %s as %s; nothing to push", items[0].Deployment, r.WorkBranch, short(w))
				return SeriesResult{Commits: []string{w}, Existing: true, OnWork: true}, nil
			}
		}
		res := SeriesResult{Commits: make([]string, len(items)), Existing: true}
		for i, it := range items {
			found, err := r.find(ctx, revertKey+it.Deployment, false)
			if err != nil {
				return SeriesResult{}, err
			}
			if found != "" {
				r.logf("Deployment %s is already reverted in %s by %s; nothing to push", it.Deployment, r.Branch, short(found))
				res.Commits[i] = found
				continue
			}
			full, ok, err := r.resolve(ctx, it.Commit)
			if err != nil {
				r.discard(ctx)
				return SeriesResult{}, err
			}
			if !ok {
				r.discard(ctx)
				return SeriesResult{}, fmt.Errorf("commit %s is not on %s (the repository does not have it)", it.Commit, r.Branch)
			}
			on, err := r.isAncestor(ctx, full, tip)
			if err != nil {
				r.discard(ctx)
				return SeriesResult{}, err
			}
			if !on {
				r.discard(ctx)
				return SeriesResult{}, fmt.Errorf("commit %s is not on %s; refusing to revert it", short(full), r.Branch)
			}
			if _, err := r.git(ctx, nil, "revert", "--no-edit", "--no-commit", full); err != nil {
				conflicted, _ := r.git(ctx, nil, "diff", "--name-only", "--diff-filter=U", "-z")
				_, _ = r.git(ctx, nil, "revert", "--abort")
				r.discard(ctx)
				if files := splitNUL(conflicted); len(files) > 0 {
					r.logf("Reverting %s conflicts with later changes in %s", short(full), r.Branch)
					return SeriesResult{}, &ConflictError{Files: files, Deployment: it.Deployment, Commit: full}
				}
				return SeriesResult{}, err
			}
			// A change that later commits already undid reverts to nothing; the
			// revert is still recorded, as an empty commit, so that it is found
			// again when retried.
			staged, err := r.hasStaged(ctx)
			if err != nil {
				r.discard(ctx)
				return SeriesResult{}, err
			}
			msg := buildMessage(it.Message, "This reverts commit "+full+".", deploymentKey+it.Deployment+"\n"+revertKey+it.Deployment)
			made, err := r.commit(ctx, msg, !staged)
			if err != nil {
				r.discard(ctx)
				return SeriesResult{}, err
			}
			res.Commits[i], res.Existing = made, false
			if staged {
				r.logf("Committed %s to %s: %s", short(made), r.Branch, subject(it.Message))
			} else {
				r.logf("Committed %s to %s: %s (later commits had already undone the change; nothing in the files changed)", short(made), r.Branch, subject(it.Message))
			}
		}
		if res.Existing {
			return res, nil
		}
		err = r.push(ctx)
		if errors.Is(err, errMoved) && try < tries {
			r.logf("%s moved on the server; fetching and reverting again (try %d of %d)", r.Branch, try+1, tries)
			continue
		}
		if errors.Is(err, errMoved) {
			r.discard(ctx)
			return SeriesResult{}, fmt.Errorf("%s kept moving on the server; gave up after %d tries to push", r.Branch, tries)
		}
		if err != nil {
			r.discard(ctx)
			return SeriesResult{}, err
		}
		return res, nil
	}
}
