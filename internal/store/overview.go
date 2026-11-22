package store

import (
	"context"
	"strconv"
)

// The statements of the repository Overview (schema 70): the cached languages of a repository, the contributors job's
// state and answer per period, and the rows of the activity feed that live in tables of their own (the audit trail's
// pushes and tags, and pull requests). Like parity.go's they run on the connection the caller hands in.

// ---- languages ----

// LanguagesGet returns the cached languages answer (JSON) and the commit it was made for.
func LanguagesGet(ctx context.Context, q CtxQueryer, repoID string) (commit, data string, ok bool, err error) {
	rows, err := q.Query(ctx, `SELECT commit_sha, data FROM repo_languages WHERE repo_id = ?`, repoID)
	if err != nil {
		return "", "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", "", false, rows.Err()
	}
	err = rows.Scan(&commit, &data)
	return commit, data, err == nil, err
}

// LanguagesPut keeps the answer for a commit, replacing the earlier one.
func LanguagesPut(ctx context.Context, x CtxExecer, repoID, commit, data string, now int64) error {
	_, err := x.Exec(ctx, `INSERT INTO repo_languages(repo_id, commit_sha, data, computed_at) VALUES(?,?,?,?)
		ON CONFLICT(repo_id) DO UPDATE SET commit_sha = excluded.commit_sha, data = excluded.data, computed_at = excluded.computed_at`,
		repoID, commit, data, now)
	return err
}

// ---- contributors ----

// ContributorsRow is the job of one repository and period.
type ContributorsRow struct {
	Commit      string
	State       string // pending, running, done or failed
	Data        string
	RequestedAt int64
	ComputedAt  int64
}

// ContributorsGet returns the row of a repository and period.
func ContributorsGet(ctx context.Context, q CtxQueryer, repoID, period string) (ContributorsRow, bool, error) {
	rows, err := q.Query(ctx, `SELECT commit_sha, state, data, requested_at, computed_at FROM repo_contributors WHERE repo_id = ? AND period = ?`, repoID, period)
	if err != nil {
		return ContributorsRow{}, false, err
	}
	defer rows.Close()
	var r ContributorsRow
	if !rows.Next() {
		return r, false, rows.Err()
	}
	err = rows.Scan(&r.Commit, &r.State, &r.Data, &r.RequestedAt, &r.ComputedAt)
	return r, err == nil, err
}

// ContributorsRequest notes that the answer for a commit is wanted: a row with another commit, or none, becomes
// pending (the old answer stays in it, marked stale by its commit, until the job replaces it). A row that is already
// pending or running for this commit, or done for it, is left as it is. It says whether the job needs to run.
func ContributorsRequest(ctx context.Context, x CtxExecer, repoID, period, commit string, now int64) (bool, error) {
	res, err := x.Exec(ctx, `INSERT INTO repo_contributors(repo_id, period, commit_sha, state, data, requested_at, computed_at) VALUES(?,?,?,'pending','{}',?,0)
		ON CONFLICT(repo_id, period) DO UPDATE SET commit_sha = excluded.commit_sha, state = 'pending', requested_at = excluded.requested_at
		WHERE repo_contributors.commit_sha <> excluded.commit_sha OR repo_contributors.state = 'failed'`,
		repoID, period, commit, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ContributorsStart marks a job as running for the commit; false when the row moved on to another commit or is done. A
// row already running is taken again: the process that holds the job guards against two at once, and one a stopped
// server left must not stay running for ever.
func ContributorsStart(ctx context.Context, x CtxExecer, repoID, period, commit string) (bool, error) {
	res, err := x.Exec(ctx, `UPDATE repo_contributors SET state = 'running' WHERE repo_id = ? AND period = ? AND commit_sha = ? AND state IN ('pending','running')`, repoID, period, commit)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ContributorsFinish keeps the answer (state done) or the failure (state failed, data holds the reason) of a job that
// was running for the commit.
func ContributorsFinish(ctx context.Context, x CtxExecer, repoID, period, commit, state, data string, now int64) error {
	_, err := x.Exec(ctx, `UPDATE repo_contributors SET state = ?, data = ?, computed_at = ? WHERE repo_id = ? AND period = ? AND commit_sha = ? AND state = 'running'`,
		state, data, now, repoID, period, commit)
	return err
}

// ---- activity ----

// ActivityRow is one thing that happened, as the tables hold it: Key is unique inside its Kind.
type ActivityRow struct {
	Kind   string // audit action, or pull_opened, pull_merged, pull_closed
	Key    string
	At     int64
	Who    string
	Target string // the repository path (audit) or repository id (pull requests)
	Detail string
	Number int // pull request number
	Title  string
}

// AuditActivity returns the audit entries of the given actions that succeeded, on the given targets, at or before
// `until` (milliseconds, 0 for no limit), newest first.
func AuditActivity(ctx context.Context, q CtxQueryer, targets, actions []string, until int64, limit int) ([]ActivityRow, error) {
	if len(targets) == 0 || len(actions) == 0 {
		return nil, nil
	}
	args := []any{}
	in := func(vals []string) string {
		s := ""
		for i, v := range vals {
			if i > 0 {
				s += ","
			}
			s += "?"
			args = append(args, v)
		}
		return s
	}
	text := `SELECT id, at, username, action, target, detail FROM audit WHERE outcome = 'ok' AND target IN (` + in(targets) + `) AND action IN (` + in(actions) + `)`
	if until > 0 {
		text += ` AND at <= ?`
		args = append(args, until)
	}
	rows, err := q.Query(ctx, text+` ORDER BY at DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityRow
	for rows.Next() {
		var r ActivityRow
		var id int64
		if err := rows.Scan(&id, &r.At, &r.Who, &r.Kind, &r.Target, &r.Detail); err != nil {
			return nil, err
		}
		r.Key = strconv.FormatInt(id, 10)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PullActivity returns when pull requests of the repositories were opened, merged and closed (without merging), at
// or before `until`, newest first.
func PullActivity(ctx context.Context, q CtxQueryer, repoIDs []string, until int64, limit int) ([]ActivityRow, error) {
	if len(repoIDs) == 0 {
		return nil, nil
	}
	args := []any{}
	ph := ""
	for i, id := range repoIDs {
		if i > 0 {
			ph += ","
		}
		ph += "?"
		args = append(args, id)
	}
	if until == 0 {
		until = 1<<62 - 1
	}
	// One row per event: the three dates of a pull request are three events.
	text := `SELECT 'pull_opened', repo_id, number, title, author, created_at FROM pull_requests WHERE repo_id IN (` + ph + `) AND created_at > 0 AND created_at <= ?
		UNION ALL SELECT 'pull_merged', repo_id, number, title, merged_by, merged_at FROM pull_requests WHERE repo_id IN (` + ph + `) AND merged_at > 0 AND merged_at <= ?
		UNION ALL SELECT 'pull_closed', repo_id, number, title, closed_by, closed_at FROM pull_requests WHERE repo_id IN (` + ph + `) AND closed_at > 0 AND merged_at = 0 AND closed_at <= ?
		ORDER BY 6 DESC, 3 DESC LIMIT ?`
	all := append(append(append(append(append(append([]any{}, args...), until), args...), until), args...), until)
	rows, err := q.Query(ctx, text, append(all, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityRow
	for rows.Next() {
		var r ActivityRow
		if err := rows.Scan(&r.Kind, &r.Target, &r.Number, &r.Title, &r.Who, &r.At); err != nil {
			return nil, err
		}
		r.Key = r.Target + "#" + strconv.Itoa(r.Number)
		out = append(out, r)
	}
	return out, rows.Err()
}
