package engine

import (
	"database/sql"
	"errors"
)

// Flight badges (table flight_badges, schema 59): which Flights show their status badge to anyone. A Flight of a
// public repository needs no row; every other Flight is off until somebody turns it on.

// FlightBadges says whether a Flight's badges are switched on.
func (e *Engine) FlightBadges(project string) (bool, error) {
	if !e.knows(schemaBadges) {
		return false, nil
	}
	var on int
	err := e.db.QueryRow(`SELECT enabled FROM flight_badges WHERE project = ?`, project).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return on == 1, err
}

// SetFlightBadges switches a Flight's badges on or off.
func (e *Engine) SetFlightBadges(project string, on bool, actor string) error {
	if !e.knows(schemaBadges) {
		return ErrConflict
	}
	_, err := e.db.Exec(`INSERT INTO flight_badges(project, enabled, updated_at, updated_by) VALUES(?,?,?,?)
		ON CONFLICT(project) DO UPDATE SET enabled = excluded.enabled, updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		project, b2i(on), e.now().UnixMilli(), actor)
	return err
}

// LastRunOn returns the newest run of a Flight on a branch, or ErrNotFound.
func (e *Engine) LastRunOn(project, branch string) (*RunSummary, error) {
	runs, err := e.ListRuns(RunFilter{Project: project, Branch: branch, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, ErrNotFound
	}
	return runs[0], nil
}
