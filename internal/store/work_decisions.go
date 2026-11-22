package store

import (
	"context"
	"database/sql"
	"strings"
)

// The statements of decision records (schema 68): a work item of type decision has a row here once it is accepted. No
// row is a proposed decision.

// DecisionRow is what a decision adds to its item.
type DecisionRow struct {
	Item         string
	Status       string // accepted or superseded
	Answer       string
	DecidedBy    string
	DecidedAt    int64
	SupersededBy string // the item id of the decision that replaced it
	SupersededAt int64
}

// DecisionStatusSQL is the status of a work item (alias i) that is a decision: its row's, or proposed.
const DecisionStatusSQL = `COALESCE((SELECT d.status FROM work_decisions d WHERE d.item_id = i.id), 'proposed')`

// WorkIsDecisionSQL is the condition of an item (alias i) that is a decision with the status given as the argument.
const WorkIsDecisionSQL = `(i.type = 'decision' AND ` + DecisionStatusSQL + ` = ?)`

// DecisionGet returns an item's decision row.
func DecisionGet(ctx context.Context, q CtxQueryer, item string) (DecisionRow, bool, error) {
	rows, err := q.Query(ctx, `SELECT item_id, status, answer, decided_by, decided_at, superseded_by, superseded_at FROM work_decisions WHERE item_id = ?`, item)
	if err != nil {
		return DecisionRow{}, false, err
	}
	defer rows.Close()
	var r DecisionRow
	if !rows.Next() {
		return r, false, rows.Err()
	}
	err = rows.Scan(&r.Item, &r.Status, &r.Answer, &r.DecidedBy, &r.DecidedAt, &r.SupersededBy, &r.SupersededAt)
	return r, err == nil, err
}

// DecisionAccept records the answer of a proposed decision; false when it already has a row.
func DecisionAccept(ctx context.Context, x interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}, item, answer, by string, at int64) (bool, error) {
	res, err := x.Exec(ctx, `INSERT INTO work_decisions(item_id, status, answer, decided_by, decided_at) VALUES(?,'accepted',?,?,?) ON CONFLICT DO NOTHING`, item, answer, by, at)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DecisionSupersede marks an accepted decision as replaced by another; false when it is not accepted.
func DecisionSupersede(ctx context.Context, x interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}, item, by string, at int64) (bool, error) {
	res, err := x.Exec(ctx, `UPDATE work_decisions SET status = 'superseded', superseded_by = ?, superseded_at = ? WHERE item_id = ? AND status = 'accepted'`, by, at, item)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DecisionsSuperseded lists the decisions that the given one replaced.
func DecisionsSuperseded(ctx context.Context, q CtxQueryer, by string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT item_id FROM work_decisions WHERE superseded_by = ? ORDER BY superseded_at, item_id`, by)
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

// DecisionListed is a decision as the list shows it, with the item's place for its reference.
type DecisionListed struct {
	DecisionRow
	ScopeKind string
	ScopeID   string
	Number    int
	Title     string
	ItemState string
}

// DecisionsList lists decisions over the items a reader may see: vis is the condition on work_items (alias i), repo
// (when not empty) keeps one repository's, status (when not empty) one status.
func DecisionsList(ctx context.Context, q CtxQueryer, vis string, visArgs []any, repo, status string, limit int) ([]DecisionListed, error) {
	where, args := []string{`i.type = 'decision'`, vis}, append([]any{}, visArgs...)
	if repo != "" {
		where, args = append(where, WorkInRepositorySQL), append(args, repo, repo)
	}
	if status != "" {
		where, args = append(where, DecisionStatusSQL+` = ?`), append(args, status)
	}
	rows, err := q.Query(ctx, `SELECT i.id, i.scope_kind, i.scope_id, i.number, i.title, i.state, `+DecisionStatusSQL+`,
		COALESCE(d.answer,''), COALESCE(d.decided_by,''), COALESCE(d.decided_at,0), COALESCE(d.superseded_by,''), COALESCE(d.superseded_at,0)
		FROM work_items i LEFT JOIN work_decisions d ON d.item_id = i.id WHERE `+strings.Join(where, " AND ")+` ORDER BY i.number, i.id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecisionListed
	for rows.Next() {
		var d DecisionListed
		if err := rows.Scan(&d.Item, &d.ScopeKind, &d.ScopeID, &d.Number, &d.Title, &d.ItemState, &d.Status, &d.Answer, &d.DecidedBy, &d.DecidedAt, &d.SupersededBy, &d.SupersededAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// WorkItemFinish moves an item to done (it keeps the time it was first closed) and counts it as changed.
func WorkItemFinish(ctx context.Context, x interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}, id string, now int64) error {
	_, err := x.Exec(ctx, `UPDATE work_items SET state = 'done', closed_at = CASE WHEN closed_at = 0 THEN ? ELSE closed_at END, updated_at = ?, version = version + 1 WHERE id = ?`, now, now, id)
	return err
}
