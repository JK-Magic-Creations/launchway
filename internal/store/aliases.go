package store

import "context"

// The old addresses of renamed and transferred hosted repositories (repo_aliases, schema 59): the address owner/name
// a repository had, kept until a new repository takes it. A row is looked up only when no live repository has the
// address, and removed when one is made or moved there.

// AddRepoAlias records that a repository used to be at path (again: the newest wins).
func AddRepoAlias(ctx context.Context, x CtxExecer, path, repoID string, at int64, by string) error {
	_, err := x.Exec(ctx, `INSERT INTO repo_aliases(path, repo_id, created_at, created_by) VALUES(?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET repo_id = excluded.repo_id, created_at = excluded.created_at, created_by = excluded.created_by`,
		path, repoID, at, by)
	return err
}

// RepoAliasTarget returns the ID of the repository that used to be at path.
func RepoAliasTarget(ctx context.Context, q CtxQueryer, path string) (string, bool, error) {
	rows, err := q.Query(ctx, `SELECT repo_id FROM repo_aliases WHERE path = ?`, path)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, rows.Err()
	}
	var id string
	return id, true, firstErr(rows.Scan(&id), rows.Err())
}

func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// DeleteRepoAliasesAt removes the alias at path: a repository has that address now.
func DeleteRepoAliasesAt(ctx context.Context, x CtxExecer, path string) error {
	_, err := x.Exec(ctx, `DELETE FROM repo_aliases WHERE path = ?`, path)
	return err
}

// RepoAliasRow is one old address.
type RepoAliasRow struct {
	Path      string
	CreatedAt int64
	CreatedBy string
}

// RepoAliasesOf lists the old addresses of a repository, newest first.
func RepoAliasesOf(ctx context.Context, q CtxQueryer, repoID string) ([]RepoAliasRow, error) {
	rows, err := q.Query(ctx, `SELECT path, created_at, created_by FROM repo_aliases WHERE repo_id = ? ORDER BY created_at DESC, path`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoAliasRow
	for rows.Next() {
		var r RepoAliasRow
		if err := rows.Scan(&r.Path, &r.CreatedAt, &r.CreatedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
