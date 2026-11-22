package engine

import (
	"context"
	"fmt"
)

// SchemaInfo compares the schema this build knows with the database's.
type SchemaInfo struct {
	Code     int `json:"code"`     // the newest schema this build knows
	Database int `json:"database"` // the schema the database is at
	// Minimum is the oldest code schema that may run against the database:
	// its newest breaking migration.
	Minimum int `json:"minimum"`
}

// Compatible reports whether this build may run against the database.
func (s SchemaInfo) Compatible() bool { return s.Code >= s.Minimum }

// Refusal explains why a build may not run against the database, or is ""
// if it may.
func (s SchemaInfo) Refusal() string {
	if s.Compatible() {
		return ""
	}
	return fmt.Sprintf("the database is at schema %d, which needs a Launchway build that knows schema %d or newer; "+
		"this build knows schema %d. Run a newer version", s.Database, s.Minimum, s.Code)
}

// ctxQuerier runs QueryRow with a context.
type ctxQuerier struct {
	d   *dbx
	ctx context.Context
}

func (q ctxQuerier) QueryRow(s string, a ...any) *rowx {
	return &rowx{row: q.d.QueryRowContext(q.ctx, rebind(q.d.pg, s), a...)}
}
