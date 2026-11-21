package work

import (
	"context"
)

// Backlog order is a whole-number rank per scope: lower is higher up. New work
// goes to the bottom with a gap of rankGap, so most moves are one update; when
// two neighbors have no room between them the scope is renumbered in the same
// transaction. Every change to ranks first locks the scope, so concurrent
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

// renumber spaces a scope's items evenly in their current order.
func (s *Store) renumber(ctx context.Context, tx DB, sc Scope) error {
	rows, err := tx.Query(ctx, `SELECT id FROM work_items WHERE scope_kind = ? AND scope_id = ? ORDER BY rank, number`, sc.Kind, sc.ID)
	if err != nil {
		return err
	}
	var idList []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		idList = append(idList, id)
	}
	rows.Close()
	for i, id := range idList {
		if _, err := tx.Exec(ctx, `UPDATE work_items SET rank = ? WHERE id = ?`, int64(i+1)*rankGap, id); err != nil {
			return err
		}
	}
	return nil
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
	for attempt := 0; attempt < 2; attempt++ {
		var target int64
		switch pos.Where {
		case "top":
			var min int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(MIN(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND id <> ?`, it.Scope.Kind, it.Scope.ID, it.ID).Scan(&min); err != nil {
				return err
			}
			target = min - rankGap
			if target < 1 {
				if err := s.renumber(ctx, tx, it.Scope); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT COALESCE(MIN(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND id <> ?`, it.Scope.Kind, it.Scope.ID, it.ID).Scan(&min); err != nil {
					return err
				}
				// Make room above the first.
				if _, err := tx.Exec(ctx, `UPDATE work_items SET rank = rank + ? WHERE scope_kind = ? AND scope_id = ?`, rankGap, it.Scope.Kind, it.Scope.ID); err != nil {
					return err
				}
				target = rankGap / 2
			}
		case "bottom":
			var max int64
			if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND id <> ?`, it.Scope.Kind, it.Scope.ID, it.ID).Scan(&max); err != nil {
				return err
			}
			target = max + rankGap
		case "before", "after":
			oid, err := s.idOf(ctx, tx, pos.Other)
			if err != nil {
				return err
			}
			var other Item
			if err := tx.QueryRow(ctx, `SELECT rank, scope_kind, scope_id FROM work_items WHERE id = ?`, oid).Scan(&other.Rank, &other.Scope.Kind, &other.Scope.ID); err != nil {
				return notFound("no item " + pos.Other)
			}
			if other.Scope != it.Scope {
				return invalid("an item is placed relative to one of the same project")
			}
			if oid == it.ID {
				return invalid("an item cannot be placed relative to itself")
			}
			var neighbor int64
			if pos.Where == "before" {
				if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND rank < ? AND id <> ?`, it.Scope.Kind, it.Scope.ID, other.Rank, it.ID).Scan(&neighbor); err != nil {
					return err
				}
				target = (neighbor + other.Rank) / 2
				if other.Rank-neighbor < 2 {
					target = -1
				}
			} else {
				if err := tx.QueryRow(ctx, `SELECT COALESCE(MIN(rank), 0) FROM work_items WHERE scope_kind = ? AND scope_id = ? AND rank > ? AND id <> ?`, it.Scope.Kind, it.Scope.ID, other.Rank, it.ID).Scan(&neighbor); err != nil {
					return err
				}
				if neighbor == 0 {
					target = other.Rank + rankGap
				} else if neighbor-other.Rank < 2 {
					target = -1
				} else {
					target = (neighbor + other.Rank) / 2
				}
			}
			if target == -1 {
				if err := s.renumber(ctx, tx, it.Scope); err != nil {
					return err
				}
				continue
			}
		default:
			return invalid("where is top, bottom, before or after")
		}
		if _, err := tx.Exec(ctx, `UPDATE work_items SET rank = ?, version = version + 1, updated_at = ? WHERE id = ?`, target, ms(s.now()), it.ID); err != nil {
			return err
		}
		return s.event(ctx, tx, it.ID, c.Actor, "ranked", 0, map[string]any{"where": pos.Where, "other": pos.Other})
	}
	return conflict("the order could not be changed; try again")
}
