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

// The Fill methods make every list of an answer an empty list, not null, so that a client reads "nothing" as an
// empty list (a page that loops over null stops drawing).

// Fill makes the lists empty rather than nil.
func (r *Reach) Fill() {
	if r.Hits == nil {
		r.Hits = []Hit{}
	}
}

// Fill makes the lists empty rather than nil.
func (r *ImpactResult) Fill() {
	if r.ByOwner == nil {
		r.ByOwner = []OwnerGroup{}
	}
	if r.Repositories == nil {
		r.Repositories = []string{}
	}
	if r.Deployments == nil {
		r.Deployments = []RunningDeployment{}
	}
}

// Fill makes the lists empty rather than nil.
func (r *OrderResult) Fill() {
	if r.Order == nil {
		r.Order = []string{}
	}
	if r.Levels == nil {
		r.Levels = [][]string{}
	}
	if r.Edges == nil {
		r.Edges = []RepoEdge{}
	}
}

// Fill makes the lists empty rather than nil.
func (r *RebuildResult) Fill() {
	if r.Items == nil {
		r.Items = []RebuildItem{}
	}
}
