package store

import (
	"context"
)

// The statements of the repository trash (schema 69): who deleted a repository and when, and the permanent removal
// of a repository's rows. The repository's Git storage (refs, packs, the pack blobs) is removed by the storage
// package in an order that survives a crash; the statements it uses are here too. Like parity.go's they run on the
// connection the caller hands in.

// TrashRow says who deleted a repository and when.
type TrashRow struct {
	RepoID         string
	DeletedAt      int64
	DeletedBy      string
	PurgeStartedAt int64
}

// RecordTrash notes that a repository was deleted (again: a later deletion of a restored repository replaces the note).
func RecordTrash(ctx context.Context, x CtxExecer, repoID string, at int64, by string) error {
	_, err := x.Exec(ctx, `INSERT INTO repo_trash(repo_id, deleted_at, deleted_by, purge_started_at) VALUES(?,?,?,0)
		ON CONFLICT(repo_id) DO UPDATE SET deleted_at = excluded.deleted_at, deleted_by = excluded.deleted_by, purge_started_at = 0`,
		repoID, at, by)
	return err
}

// TrashRows returns the notes of every deleted repository, by repository.
func TrashRows(ctx context.Context, q CtxQueryer) (map[string]TrashRow, error) {
	rows, err := q.Query(ctx, `SELECT repo_id, deleted_at, deleted_by, purge_started_at FROM repo_trash`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TrashRow{}
	for rows.Next() {
		var r TrashRow
		if err := rows.Scan(&r.RepoID, &r.DeletedAt, &r.DeletedBy, &r.PurgeStartedAt); err != nil {
			return nil, err
		}
		out[r.RepoID] = r
	}
	return out, rows.Err()
}

// ClearTrash forgets the note of a repository (it was restored).
func ClearTrash(ctx context.Context, x CtxExecer, repoID string) error {
	_, err := x.Exec(ctx, `DELETE FROM repo_trash WHERE repo_id = ?`, repoID)
	return err
}

// MarkPurgeStarted records that the permanent removal of a repository began, so that one that stopped half way is
// found and finished. A repository with no note gets one.
func MarkPurgeStarted(ctx context.Context, x CtxExecer, repoID string, at int64) error {
	_, err := x.Exec(ctx, `INSERT INTO repo_trash(repo_id, deleted_at, deleted_by, purge_started_at) VALUES(?,?,'',?)
		ON CONFLICT(repo_id) DO UPDATE SET purge_started_at = excluded.purge_started_at WHERE repo_trash.purge_started_at = 0`,
		repoID, at, at)
	return err
}

// PurgingRepositories lists repositories whose removal began and did not finish (a crash): the worker finishes them
// whatever the retention.
func PurgingRepositories(ctx context.Context, q CtxQueryer) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT repo_id FROM repo_trash WHERE purge_started_at > 0 ORDER BY purge_started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- the Git storage of a repository (ledger tables) ----

// PackRow is a row of repo_packs, as far as removal needs it.
type PackRow struct {
	ID, BlobKey string
	Bytes       int64
}

// PacksOf lists every pack row of a repository, in whatever state.
func PacksOf(ctx context.Context, q CtxQueryer, repoID string) ([]PackRow, error) {
	rows, err := q.Query(ctx, `SELECT pack_id, blob_key, bytes FROM repo_packs WHERE repo_id = ? ORDER BY pack_id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackRow
	for rows.Next() {
		var p PackRow
		if err := rows.Scan(&p.ID, &p.BlobKey, &p.Bytes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// BlobUsedElsewhere says whether a pack row of another repository names the same blob.
func BlobUsedElsewhere(ctx context.Context, q CtxQueryer, repoID, blobKey string) (bool, error) {
	rows, err := q.Query(ctx, `SELECT 1 FROM repo_packs WHERE blob_key = ? AND repo_id <> ? LIMIT 1`, blobKey, repoID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// DeletePackRow removes one pack row.
func DeletePackRow(ctx context.Context, x CtxExecer, repoID, packID string) error {
	_, err := x.Exec(ctx, `DELETE FROM repo_packs WHERE repo_id = ? AND pack_id = ?`, repoID, packID)
	return err
}

// DeleteRefRows removes every ref of a repository.
func DeleteRefRows(ctx context.Context, x CtxExecer, repoID string) error {
	_, err := x.Exec(ctx, `DELETE FROM repo_refs WHERE repo_id = ?`, repoID)
	return err
}

// FlagRepoStorage sets the reason on a repository's generation row, which stops maintenance from touching it.
func FlagRepoStorage(ctx context.Context, x CtxExecer, repoID, why string) error {
	_, err := x.Exec(ctx, `UPDATE repo_generation SET flagged = ? WHERE repo_id = ?`, why, repoID)
	return err
}

// DeleteRepoLedger removes the generation and maintenance rows: the last step of removing a repository's storage,
// after which the ledger no longer knows it.
func DeleteRepoLedger(ctx context.Context, x CtxExecer, repoID string) error {
	if _, err := x.Exec(ctx, `DELETE FROM repo_maintenance WHERE repo_id = ?`, repoID); err != nil {
		return err
	}
	_, err := x.Exec(ctx, `DELETE FROM repo_generation WHERE repo_id = ?`, repoID)
	return err
}

// ---- the repository's other rows ----

// repoOwnedTables are the tables whose rows belong to one repository and go with it, named by the column that holds
// the repository's ID. The pull request tables are removed through their pull requests.
var repoOwnedTables = []struct{ table, column string }{
	{"branch_rules", "repo_id"}, {"branch_writers", "repo_id"}, {"tag_rules", "repo_id"}, {"repo_mirrors", "repo_id"},
	{"repo_webhooks", "repo_id"}, {"ssh_keys", "repo_id"}, {"repo_pull_settings", "repo_id"}, {"pull_counters", "repo_id"},
	{"pull_queue", "repo_id"}, {"imported_comments", "repo_id"}, {"imported_issues", "repo_id"}, {"repo_aliases", "repo_id"},
}

// pullChildTables hold a pull request's ID in pull_id.
var pullChildTables = []string{"pull_people", "pull_revisions", "pull_runs", "pull_reviews", "pull_comments", "pull_viewed"}

// RepoOwnedTables names the tables DeleteRepoRows clears, so a test can check that every table that holds a
// repository's ID is either here or accounted for.
func RepoOwnedTables() []string {
	out := make([]string, 0, len(repoOwnedTables)+len(pullChildTables)+1)
	for _, t := range repoOwnedTables {
		out = append(out, t.table)
	}
	return append(append(out, pullChildTables...), "pull_requests")
}

// DeleteRepoRows removes a trashed repository's own rows (rules, keys, hooks, mirrors, pull requests and their
// children) and, last, the repository row and its trash note. It refuses a repository that is not in the trash. Every
// statement can run again: removal that stopped half way is finished by running it once more.
func DeleteRepoRows(ctx context.Context, x CtxExecer, repoID, trashPrefix string) (bool, error) {
	for _, c := range pullChildTables {
		if _, err := x.Exec(ctx, `DELETE FROM `+c+` WHERE pull_id IN (SELECT id FROM pull_requests WHERE repo_id = ?)`, repoID); err != nil {
			return false, err
		}
	}
	if _, err := x.Exec(ctx, `DELETE FROM pull_requests WHERE repo_id = ?`, repoID); err != nil {
		return false, err
	}
	for _, t := range repoOwnedTables {
		if _, err := x.Exec(ctx, `DELETE FROM `+t.table+` WHERE `+t.column+` = ?`, repoID); err != nil {
			return false, err
		}
	}
	res, err := x.Exec(ctx, `DELETE FROM forge_repositories WHERE id = ? AND path LIKE ?`, repoID, trashPrefix+"%")
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if _, err := x.Exec(ctx, `DELETE FROM repo_trash WHERE repo_id = ?`, repoID); err != nil {
		return n > 0, err
	}
	return n > 0, nil
}
