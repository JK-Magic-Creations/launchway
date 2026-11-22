package engine

import (
	"fmt"

	"github.com/jk-magic-creations/launchway/internal/store"
)

// The Flights kept on an outside Git host (schema 57; internal/store/outside_flights.go).

// OutsideFlight is a project attached to a repository on a connection.
type OutsideFlight = store.FlightRow

func (e *Engine) needHosts() error {
	if !e.knows(schemaHosts) {
		return fmt.Errorf("%w: this database is older than the outside-host connection (schema %d)", ErrConflict, schemaHosts)
	}
	return nil
}

// PutOutsideFlight attaches a project to a repository of a connection (by ID).
func (e *Engine) PutOutsideFlight(project, connection, repo string) error {
	if err := e.needHosts(); err != nil {
		return err
	}
	defer e.writeLock()()
	return store.PutFlight(e.db, project, connection, repo, e.now().UnixMilli())
}

// DeleteOutsideFlight detaches a project; false when it was not attached.
func (e *Engine) DeleteOutsideFlight(project string) (bool, error) {
	if err := e.needHosts(); err != nil {
		return false, err
	}
	defer e.writeLock()()
	return store.DeleteFlight(e.db, project)
}

// SetOutsideReports sets a Flight's two report switches.
func (e *Engine) SetOutsideReports(project string, statuses, comment bool) error {
	if err := e.needHosts(); err != nil {
		return err
	}
	defer e.writeLock()()
	return store.SetFlightReports(e.db, project, statuses, comment, e.now().UnixMilli())
}

// OutsideFlightOf returns a project's attachment; false when it has none.
func (e *Engine) OutsideFlightOf(project string) (OutsideFlight, bool, error) {
	if !e.knows(schemaHosts) {
		return OutsideFlight{}, false, nil
	}
	return store.FlightOf(e.db, project)
}

// OutsideFlightsOfRepository lists the projects attached to a repository of a connection.
func (e *Engine) OutsideFlightsOfRepository(connection, repo string) ([]OutsideFlight, error) {
	if !e.knows(schemaHosts) {
		return nil, nil
	}
	return store.FlightsOfRepository(e.db, connection, repo)
}

// OutsideFlightsOfConnection lists the projects attached to a connection.
func (e *Engine) OutsideFlightsOfConnection(connection string) ([]OutsideFlight, error) {
	if !e.knows(schemaHosts) {
		return nil, nil
	}
	return store.FlightsOfConnection(e.db, connection)
}

// OutsideSummaryComment returns the key of a pull request's summary comment ("" when none).
func (e *Engine) OutsideSummaryComment(project string, number int) (string, error) {
	if !e.knows(schemaHosts) {
		return "", nil
	}
	return store.SummaryComment(e.db, project, number)
}

// PutOutsideSummaryComment remembers the summary comment of a pull request.
func (e *Engine) PutOutsideSummaryComment(project string, number int, key string) error {
	if err := e.needHosts(); err != nil {
		return err
	}
	defer e.writeLock()()
	return store.PutSummaryComment(e.db, project, number, key, e.now().UnixMilli())
}
