package store

import (
	"context"
	"database/sql"
	"strings"
)

// The statements of the time logged on work items (schema 68): timers and manual entries, one running timer per
// person, and the totals and the export over the items a caller may see. The work package passes the visibility
// condition of its own list (alias i) so that nothing here decides who may read what.

// TimeRow is one entry of work_time_entries. EndedAt is 0 while a timer runs.
type TimeRow struct {
	ID, Item, Who, Kind, Day string
	StartedAt, EndedAt       int64
	Minutes                  int
	Note                     string
	CreatedAt, UpdatedAt     int64
}

// TimeExecer runs a statement.
type TimeExecer interface {
	Exec(ctx context.Context, q string, args ...any) (sql.Result, error)
}

const timeCols = `id, item_id, who, kind, day, started_at, ended_at, minutes, note, created_at, updated_at`

func scanTime(r interface{ Scan(...any) error }) (TimeRow, error) {
	var t TimeRow
	err := r.Scan(&t.ID, &t.Item, &t.Who, &t.Kind, &t.Day, &t.StartedAt, &t.EndedAt, &t.Minutes, &t.Note, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func timeRows(rows *sql.Rows, err error) ([]TimeRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimeRow
	for rows.Next() {
		t, err := scanTime(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TimeInsert adds an entry.
func TimeInsert(ctx context.Context, x TimeExecer, t TimeRow) error {
	_, err := x.Exec(ctx, `INSERT INTO work_time_entries(`+timeCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Item, t.Who, t.Kind, t.Day, t.StartedAt, t.EndedAt, t.Minutes, t.Note, t.CreatedAt, t.UpdatedAt)
	return err
}

// TimeGet returns one entry.
func TimeGet(ctx context.Context, q CtxQueryer, id string) (TimeRow, bool, error) {
	rows, err := timeRows(q.Query(ctx, `SELECT `+timeCols+` FROM work_time_entries WHERE id = ?`, id))
	if err != nil || len(rows) == 0 {
		return TimeRow{}, false, err
	}
	return rows[0], true, nil
}

// TimeRunning returns a person's running timer.
func TimeRunning(ctx context.Context, q CtxQueryer, who string) (TimeRow, bool, error) {
	rows, err := timeRows(q.Query(ctx, `SELECT `+timeCols+` FROM work_time_entries WHERE who = ? AND kind = 'timer' AND ended_at = 0`, who))
	if err != nil || len(rows) == 0 {
		return TimeRow{}, false, err
	}
	return rows[0], true, nil
}

// TimeRunningOn returns the running timers on an item.
func TimeRunningOn(ctx context.Context, q CtxQueryer, item string) ([]TimeRow, error) {
	return timeRows(q.Query(ctx, `SELECT `+timeCols+` FROM work_time_entries WHERE item_id = ? AND kind = 'timer' AND ended_at = 0 ORDER BY started_at`, item))
}

// TimeFinish stops a running timer, giving the minutes it counts for; false when it was not running.
func TimeFinish(ctx context.Context, x TimeExecer, id string, endedAt int64, minutes int, note string) (bool, error) {
	res, err := x.Exec(ctx, `UPDATE work_time_entries SET ended_at = ?, minutes = ?, note = CASE WHEN ? <> '' THEN ? ELSE note END, updated_at = ? WHERE id = ? AND kind = 'timer' AND ended_at = 0`,
		endedAt, minutes, note, note, endedAt, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TimeUpdate changes one of a person's own finished entries; false when there is none such.
func TimeUpdate(ctx context.Context, x TimeExecer, id, who string, minutes int, note, day string, now int64) (bool, error) {
	res, err := x.Exec(ctx, `UPDATE work_time_entries SET minutes = ?, note = ?, day = ?, updated_at = ? WHERE id = ? AND who = ? AND NOT (kind = 'timer' AND ended_at = 0)`,
		minutes, note, day, now, id, who)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TimeDelete removes one of a person's own entries (a running timer too: that discards it).
func TimeDelete(ctx context.Context, x TimeExecer, id, who string) (bool, error) {
	res, err := x.Exec(ctx, `DELETE FROM work_time_entries WHERE id = ? AND who = ?`, id, who)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TimeOfItem lists an item's finished entries, newest day first.
func TimeOfItem(ctx context.Context, q CtxQueryer, item string) ([]TimeRow, error) {
	return timeRows(q.Query(ctx, `SELECT `+timeCols+` FROM work_time_entries WHERE item_id = ? AND NOT (kind = 'timer' AND ended_at = 0) ORDER BY day DESC, created_at DESC, id`, item))
}

// TimeFilter narrows the entries read across items: Visible is the condition on work_items (alias i) of what the
// reader may see, with its arguments.
type TimeFilter struct {
	Visible     string
	VisibleArgs []any
	Who         string
	FromDay     string // inclusive, YYYY-MM-DD; "" for none
	ToDay       string // inclusive
}

// TimeAcross lists finished entries over the items the filter lets the reader see, with each item's place for its
// reference and its title.
type TimeAcross struct {
	TimeRow
	Scope, ScopeID string
	Number         int
	Title          string
}

func TimeEntries(ctx context.Context, q CtxQueryer, f TimeFilter, limit int) ([]TimeAcross, error) {
	where, args := []string{f.Visible, `NOT (t.kind = 'timer' AND t.ended_at = 0)`}, append([]any{}, f.VisibleArgs...)
	if f.Who != "" {
		where, args = append(where, `t.who = ?`), append(args, f.Who)
	}
	if f.FromDay != "" {
		where, args = append(where, `t.day >= ?`), append(args, f.FromDay)
	}
	if f.ToDay != "" {
		where, args = append(where, `t.day <= ?`), append(args, f.ToDay)
	}
	rows, err := q.Query(ctx, `SELECT t.id, t.item_id, t.who, t.kind, t.day, t.started_at, t.ended_at, t.minutes, t.note, t.created_at, t.updated_at, i.scope_kind, i.scope_id, i.number, i.title
		FROM work_time_entries t JOIN work_items i ON i.id = t.item_id WHERE `+strings.Join(where, " AND ")+` ORDER BY t.day DESC, t.created_at DESC, t.id LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimeAcross
	for rows.Next() {
		var a TimeAcross
		t := &a.TimeRow
		if err := rows.Scan(&t.ID, &t.Item, &t.Who, &t.Kind, &t.Day, &t.StartedAt, &t.EndedAt, &t.Minutes, &t.Note, &t.CreatedAt, &t.UpdatedAt, &a.Scope, &a.ScopeID, &a.Number, &a.Title); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
