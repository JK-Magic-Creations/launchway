package sealing

import (
	"context"
	"database/sql"
)

// eachRow runs a query and calls scan for every row, closing the rows and
// returning the first error from the query, a scan or the iteration.
func eachRow(ctx context.Context, db DB, q string, args []any, scan func(*sql.Rows) error) error {
	rs, err := db.Query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		if err := scan(rs); err != nil {
			return err
		}
	}
	return rs.Err()
}
