package store

import (
	"context"
	"database/sql"
	"strings"
)

// The statements of the work features added with schema 68: the repository condition of a work list, and reactions.
// The work package calls them with the connection or transaction it holds.

// WorkInRepositorySQL is the condition of a work item (alias i) that belongs to a repository: it lives in the
// repository's own scope, or it is tied to code in the repository. It takes the repository path three times (scope,
// then code link).
const WorkInRepositorySQL = `((i.scope_kind = 'project' AND i.scope_id = ?) OR EXISTS (SELECT 1 FROM work_code_links rl WHERE rl.item_id = i.id AND rl.repo = ?))`

// ---- reactions ----

// ReactionRow is how many people made a reaction on a target, and whether the asker is one of them.
type ReactionRow struct {
	Target   string
	Reaction string
	Count    int
	Mine     bool
}

// ReactionSet records a person's reaction (again: nothing changes); it says whether it was new.
func ReactionSet(ctx context.Context, x interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}, kind, target, who, reaction string, now int64) (bool, error) {
	res, err := x.Exec(ctx, `INSERT INTO work_reactions(target_kind, target_id, who, reaction, created_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, kind, target, who, reaction, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReactionClear removes a person's reaction; it says whether there was one.
func ReactionClear(ctx context.Context, x interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}, kind, target, who, reaction string) (bool, error) {
	res, err := x.Exec(ctx, `DELETE FROM work_reactions WHERE target_kind = ? AND target_id = ? AND who = ? AND reaction = ?`, kind, target, who, reaction)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReactionCounts counts the reactions on the targets of one kind, by target and reaction, and marks the ones `me` made.
func ReactionCounts(ctx context.Context, q CtxQueryer, kind string, targets []string, me string) ([]ReactionRow, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	args := []any{me, kind}
	for _, t := range targets {
		args = append(args, t)
	}
	rows, err := q.Query(ctx, `SELECT target_id, reaction, COUNT(*), SUM(CASE WHEN who = ? THEN 1 ELSE 0 END) FROM work_reactions WHERE target_kind = ? AND target_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(targets)), ",")+`) GROUP BY target_id, reaction ORDER BY target_id, reaction`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReactionRow
	for rows.Next() {
		var r ReactionRow
		var mine int
		if err := rows.Scan(&r.Target, &r.Reaction, &r.Count, &mine); err != nil {
			return nil, err
		}
		r.Mine = mine > 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// WorkCommentItem returns the item a comment belongs to.
func WorkCommentItem(ctx context.Context, q CtxQueryer, commentID string) (string, bool, error) {
	rows, err := q.Query(ctx, `SELECT item_id FROM work_comments WHERE id = ?`, commentID)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, rows.Err()
	}
	var id string
	err = rows.Scan(&id)
	return id, err == nil, err
}
