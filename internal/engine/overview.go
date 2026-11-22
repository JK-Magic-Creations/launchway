package engine

import (
	"context"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// What the repository Overview keeps in the database (schema 70) and the rows its activity feed reads from the audit
// trail and the pull request table. The statements are in internal/store; the way to them is here.

// ContributorsJob names one job of the contributors table.
type ContributorsJob struct{ RepoID, Period, Commit string }

// LanguagesCached returns the cached languages answer (JSON) and the commit it was made for.
func (e *Engine) LanguagesCached(ctx context.Context, repoID string) (commit, data string, ok bool, err error) {
	if !e.knows(schemaOverview) {
		return "", "", false, nil
	}
	return store.LanguagesGet(ctx, e.Database(), repoID)
}

// SaveLanguages keeps the answer for a commit.
func (e *Engine) SaveLanguages(ctx context.Context, repoID, commit, data string) error {
	if !e.knows(schemaOverview) {
		return nil
	}
	defer e.writeLock()()
	return store.LanguagesPut(ctx, e.Database(), repoID, commit, data, e.now().UnixMilli())
}

// ContributorsRow returns the job row of a repository and period.
func (e *Engine) ContributorsRow(ctx context.Context, repoID, period string) (store.ContributorsRow, bool, error) {
	if !e.knows(schemaOverview) {
		return store.ContributorsRow{}, false, nil
	}
	return store.ContributorsGet(ctx, e.Database(), repoID, period)
}

// RequestContributors asks for the answer for a commit; it says whether a job has to be started.
func (e *Engine) RequestContributors(ctx context.Context, repoID, period, commit string) (bool, error) {
	if !e.knows(schemaOverview) {
		return false, ErrConflict
	}
	defer e.writeLock()()
	return store.ContributorsRequest(ctx, e.Database(), repoID, period, commit, e.now().UnixMilli())
}

// StartContributors takes a pending job; false when it is not pending any more.
func (e *Engine) StartContributors(ctx context.Context, j ContributorsJob) (bool, error) {
	defer e.writeLock()()
	return store.ContributorsStart(ctx, e.Database(), j.RepoID, j.Period, j.Commit)
}

// FinishContributors keeps the result of a job that was running.
func (e *Engine) FinishContributors(ctx context.Context, j ContributorsJob, state, data string) error {
	defer e.writeLock()()
	return store.ContributorsFinish(ctx, e.Database(), j.RepoID, j.Period, j.Commit, state, data, e.now().UnixMilli())
}

// AuditActivity returns the successful audit entries of the actions on the targets, newest first.
func (e *Engine) AuditActivity(ctx context.Context, targets, actions []string, who string, until int64, limit int) ([]store.ActivityRow, error) {
	return store.AuditActivity(ctx, e.Database(), targets, actions, who, until, limit)
}

// PullActivity returns when the pull requests of the repositories were opened, merged and closed.
func (e *Engine) PullActivity(ctx context.Context, repoIDs []string, who string, until int64, limit int) ([]store.ActivityRow, error) {
	return store.PullActivity(ctx, e.Database(), repoIDs, who, until, limit)
}
