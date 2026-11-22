package store

import "database/sql"

// The Flights kept on an outside Git host (schema 57). outside_flights ties a
// project to one repository on a connection and holds its two report switches;
// outside_comments remembers the one summary comment of a pull request so it
// is edited in place. Neither holds a credential. Until the engine moves into
// the store (stage S2) the statements run on the connection the engine hands in.

// FlightRow is one row of outside_flights.
type FlightRow struct {
	Project        string
	ConnectionID   string
	Repository     string
	ReportStatuses bool
	ReportComment  bool
}

const flightCols = `project, connection_id, repo_path, report_statuses, report_comment`

func scanFlight(sc interface{ Scan(...any) error }) (FlightRow, error) {
	var f FlightRow
	var st, cm int
	if err := sc.Scan(&f.Project, &f.ConnectionID, &f.Repository, &st, &cm); err != nil {
		return f, err
	}
	f.ReportStatuses, f.ReportComment = st != 0, cm != 0
	return f, nil
}

// PutFlight attaches a project to a repository, keeping its switches when it is already attached.
func PutFlight(x Execer, project, connection, repo string, now int64) error {
	_, err := x.Exec(`INSERT INTO outside_flights(project, connection_id, repo_path, created_at, updated_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET connection_id = excluded.connection_id, repo_path = excluded.repo_path, updated_at = excluded.updated_at`,
		project, connection, repo, now, now)
	return err
}

// DeleteFlight detaches a project and forgets its summary comments; false when it was not attached.
func DeleteFlight(x Execer, project string) (bool, error) {
	res, err := x.Exec(`DELETE FROM outside_flights WHERE project = ?`, project)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	_, err = x.Exec(`DELETE FROM outside_comments WHERE project = ?`, project)
	return n > 0, err
}

// SetFlightReports sets the two report switches.
func SetFlightReports(x Execer, project string, statuses, comment bool, now int64) error {
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	_, err := x.Exec(`UPDATE outside_flights SET report_statuses = ?, report_comment = ?, updated_at = ? WHERE project = ?`, b(statuses), b(comment), now, project)
	return err
}

func queryFlights(q Queryer, where string, args ...any) ([]FlightRow, error) {
	rows, err := q.Query(`SELECT `+flightCols+` FROM outside_flights `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FlightRow
	for rows.Next() {
		f, err := scanFlight(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FlightOf returns the attachment of a project; false when it has none.
func FlightOf(q Queryer, project string) (FlightRow, bool, error) {
	fs, err := queryFlights(q, `WHERE project = ?`, project)
	if err != nil || len(fs) == 0 {
		return FlightRow{}, false, err
	}
	return fs[0], true, nil
}

// FlightsOfRepository lists the projects attached to a repository of a connection (the path compared without regard to case).
func FlightsOfRepository(q Queryer, connection, repo string) ([]FlightRow, error) {
	return queryFlights(q, `WHERE connection_id = ? AND lower(repo_path) = lower(?) ORDER BY project`, connection, repo)
}

// FlightsOfConnection lists the projects attached to a connection.
func FlightsOfConnection(q Queryer, connection string) ([]FlightRow, error) {
	return queryFlights(q, `WHERE connection_id = ? ORDER BY project`, connection)
}

// SummaryComment returns the key of a pull request's summary comment, "" when there is none.
func SummaryComment(q Queryer, project string, number int) (string, error) {
	rows, err := q.Query(`SELECT comment_id FROM outside_comments WHERE project = ? AND pr_number = ?`, project, number)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	key := ""
	if rows.Next() {
		if err := rows.Scan(&key); err != nil {
			return "", err
		}
	}
	return key, rows.Err()
}

// PutSummaryComment remembers the summary comment of a pull request.
func PutSummaryComment(x Execer, project string, number int, key string, now int64) error {
	_, err := x.Exec(`INSERT INTO outside_comments(project, pr_number, comment_id, updated_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(project, pr_number) DO UPDATE SET comment_id = excluded.comment_id, updated_at = excluded.updated_at`, project, number, key, now)
	return err
}

// LastDeliveryAt is when the connection last received a webhook delivery (Unix ms); false when none is on record.
func LastDeliveryAt(q Queryer, connection string) (int64, bool, error) {
	rows, err := q.Query(`SELECT MAX(at) FROM forge_deliveries WHERE connection_id = ?`, connection)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	var at sql.NullInt64
	if rows.Next() {
		if err := rows.Scan(&at); err != nil {
			return 0, false, err
		}
	}
	return at.Int64, at.Valid, rows.Err()
}
