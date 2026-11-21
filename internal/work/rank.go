package work

import (
	"context"
)

// Backlog order is a whole-number rank per scope: lower is higher up. New work
// goes to the bottom with a gap of rankGap, so most moves are one update; when
// two neighbors have no room between them, everything above the gap moves up
// by one gap in the same statement. Every change to ranks first locks the scope, so concurrent
// moves queue instead of colliding.
const rankGap = 1024

func (s *Store) lockScope(ctx context.Context, tx DB, sc Scope) error {
	if err := s.ensureScope(ctx, tx, sc); err != nil {
		return err
	}
	var n int
	return tx.QueryRow(ctx, `SELECT next_number FROM work_scopes WHERE scope_kind = ? AND scope_id = ?`+s.forUpdate(), sc.Kind, sc.ID).Scan(&n)
}

func (s *Store) nextRank(ctx context.Context, tx DB, sc Scope) (int64, error) {
	var max int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ?`, sc.Kind, sc.ID).Scan(&max); err != nil {
		return 0, err
	}
	return max + rankGap, nil
}

// Position says where a moved item goes.
type Position struct {
	Where string `json:"where"` // top, bottom, before or after
	Other string `json:"other"` // a ref, for before and after
}

// Rank moves an item within its scope's order.
func (s *Store) Rank(ctx context.Context, c Caller, ref string, pos Position) (*Item, error) {
	it, err := s.Get(ctx, c, ref)
	if err != nil {
		return nil, err
	}
	if !c.canWrite(it.Scope) {
		return nil, forbidden("you may not reorder work in %s", it.Scope)
	}
	if c.Agent != nil {
		return nil, forbidden("agents do not reorder the backlog")
	}
	err = s.tx(ctx, func(tx DB) error {
		if err := s.lockScope(ctx, tx, it.Scope); err != nil {
			return err
		}
		return s.placeRank(ctx, tx, c, it, pos)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, c, it.ID)
}

func (s *Store) placeRank(ctx context.Context, tx DB, c Caller, it *Item, pos Position) error {
	var target int64
	one := func(q string, a ...any) (int64, error) {
		var v int64
		err := tx.QueryRow(ctx, q, a...).Scan(&v)
		return v, err
	}
	switch pos.Where {
	case "top":
		min, err := one(`SELECT COALESCE(MIN(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND id <> ?`, it.Scope.Kind, it.Scope.ID, it.ID)
		if err != nil {
			return err
		}
		target = min - rankGap // ranks may go below zero: only their order matters
	case "bottom":
		max, err := one(`SELECT COALESCE(MAX(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND id <> ?`, it.Scope.Kind, it.Scope.ID, it.ID)
		if err != nil {
			return err
		}
		target = max + rankGap
	case "before", "after":
		oid, err := s.idOf(ctx, tx, pos.Other)
		if err != nil {
			return err
		}
		if oid == it.ID {
			return invalid("an item cannot be placed relative to itself")
		}
		var osc Scope
		var orank int64
		if err := tx.QueryRow(ctx, `SELECT rank, scope_kind, scope_id FROM work_items WHERE id = ?`, oid).Scan(&orank, &osc.Kind, &osc.ID); err != nil {
			return notFound("no item " + pos.Other)
		}
		if osc != it.Scope {
			return invalid("an item is placed relative to one of the same project")
		}
		target, err = s.betweenRank(ctx, tx, it, orank, pos.Where == "before")
		if err != nil {
			return err
		}
	default:
		return invalid("where is top, bottom, before or after")
	}
	if _, err := tx.Exec(ctx, `UPDATE work_items SET rank = ?, version = version + 1, updated_at = ? WHERE id = ?`, target, ms(s.now()), it.ID); err != nil {
		return err
	}
	return s.event(ctx, tx, it.ID, c.Actor, "ranked", 0, map[string]any{"where": pos.Where, "other": pos.Other})
}

// betweenRank finds a rank next to another item's: halfway to its neighbor,
// and when the two touch, after making room by moving everything above the
// gap up in one statement (the order is kept).
func (s *Store) betweenRank(ctx context.Context, tx DB, it *Item, orank int64, before bool) (int64, error) {
	var neighbor int64
	var has int
	q := `SELECT COUNT(*), COALESCE(MAX(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND rank < ? AND id <> ?`
	if !before {
		q = `SELECT COUNT(*), COALESCE(MIN(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND rank > ? AND id <> ?`
	}
	if err := tx.QueryRow(ctx, q, it.Scope.Kind, it.Scope.ID, orank, it.ID).Scan(&has, &neighbor); err != nil {
		return 0, err
	}
	switch {
	case has == 0 && before:
		return orank - rankGap, nil
	case has == 0:
		return orank + rankGap, nil
	}
	low, high := neighbor, orank
	if !before {
		low, high = orank, neighbor
	}
	if high-low < 2 {
		if _, err := tx.Exec(ctx, `UPDATE work_items SET rank = rank + ? WHERE scope_kind = ? AND scope_id = ? AND rank >= ? AND id <> ?`, rankGap, it.Scope.Kind, it.Scope.ID, high, it.ID); err != nil {
			return 0, err
		}
		high += rankGap
	}
	return (low + high) / 2, nil
}
