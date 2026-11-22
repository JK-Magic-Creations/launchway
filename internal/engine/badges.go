package engine

import "github.com/jk-magic-creations/launchway/internal/store"

// Flight badges (table flight_badges, schema 59): which Flights show their status badge to anyone. A Flight of a
// public repository needs no row; every other Flight is off until somebody turns it on.

// FlightBadges says whether a Flight's badges are switched on.
func (e *Engine) FlightBadges(project string) (bool, error) {
	if !e.knows(schemaBadges) {
		return false, nil
	}
	return store.FlightBadgesOn(e.db, project)
}

// SetFlightBadges switches a Flight's badges on or off.
func (e *Engine) SetFlightBadges(project string, on bool, actor string) error {
	if !e.knows(schemaBadges) {
		return ErrConflict
	}
	return store.SetFlightBadgesOn(e.db, project, on, e.now().UnixMilli(), actor)
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
