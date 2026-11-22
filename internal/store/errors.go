package store

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound is the one answer for "no such thing in this Base",
	// whether it does not exist or belongs to another Base: the two are
	// indistinguishable by design.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the change clashes with what is already there.
	ErrConflict = errors.New("conflict")
	// ErrDisabled means the operation needs the multi_base switch.
	ErrDisabled = errors.New("several Bases are not turned on (multi_base)")
)

// IsConflict reports whether err means another transaction got there first
// or a unique rule was broken, so the work should be retried against fresh
// state or refused.
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", // unique violation
			"40001", // serialization failure
			"40P01": // deadlock detected
			return true
		}
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed") // SQLite
}
