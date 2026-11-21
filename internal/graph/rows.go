package graph

import (
	"context"
	"database/sql"
	"errors"
)

// errStop ends eachRow early without an error.
var errStop = errors.New("stop")

// queryer is the database or a transaction.
type queryer interface {
	Query(ctx context.Context, q string, a ...any) (*sql.Rows, error)
}

// eachRow runs a query and calls fn for each row, closing the rows and
// checking their error. fn may return errStop to end early.
func eachRow(ctx context.Context, db queryer, q string, args []any, fn func(rows *sql.Rows) error) error {
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			if errors.Is(err, errStop) {
				return nil
			}
			return err
		}
	}
	return rows.Err()
}

// ids runs a query that returns one number per row.
func ids(ctx context.Context, db queryer, q string, args ...any) ([]int64, error) {
	var out []int64
	err := eachRow(ctx, db, q, args, func(rows *sql.Rows) error {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		out = append(out, id)
		return nil
	})
	return out, err
}
