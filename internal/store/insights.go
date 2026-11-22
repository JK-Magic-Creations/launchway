package store

// The queries behind the Insights pages. They read what the product already
// keeps (runs, steps, test summaries) and return rows for the engine to
// aggregate; they never return a log line, a commit message or a person.
// Like the tables they read, they run on the connection the engine hands in
// until the engine moves into the store (stage S2). Every query is bounded by
// a time range on an indexed column and by a row limit.

// InsightRun is one run's timing and outcome.
type InsightRun struct {
	Project                          string
	Status                           string
	CreatedAt, StartedAt, FinishedAt int64
}

// InsightRuns returns the runs created in [from, to) (Unix milliseconds), oldest
// first, for one Flight (project "" means any) and one branch ("" means any),
// at most limit of them.
func InsightRuns(q Queryer, project, branch string, from, to int64, limit int) ([]InsightRun, error) {
	query := `SELECT project, status, created_at, started_at, finished_at FROM runs WHERE created_at >= ? AND created_at < ?`
	args := []any{from, to}
	if project != "" {
		query += ` AND project = ?`
		args = append(args, project)
	}
	if branch != "" {
		query += ` AND branch = ?`
		args = append(args, branch)
	}
	query += ` ORDER BY created_at LIMIT ?`
	args = append(args, limit)
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InsightRun
	for rows.Next() {
		var r InsightRun
		if err := rows.Scan(&r.Project, &r.Status, &r.CreatedAt, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsightStep is one step's timing and outcome.
type InsightStep struct {
	Project                         string
	Step                            string
	Status                          string
	Attempt                         int
	QueuedAt, StartedAt, FinishedAt int64
	RunCreatedAt                    int64
}

// InsightSteps returns the steps of the runs created in [from, to) that have finished,
// for one Flight and one branch (each "" for any), at most limit of them.
func InsightSteps(q Queryer, project, branch string, from, to int64, limit int) ([]InsightStep, error) {
	query := `SELECT r.project, s.step_id, s.status, s.attempt, s.queued_at, s.started_at, s.finished_at, r.created_at
		FROM steps s JOIN runs r ON r.id = s.run_id
		WHERE r.created_at >= ? AND r.created_at < ? AND s.status IN ('succeeded', 'failed')`
	args := []any{from, to}
	if project != "" {
		query += ` AND r.project = ?`
		args = append(args, project)
	}
	if branch != "" {
		query += ` AND r.branch = ?`
		args = append(args, branch)
	}
	query += ` ORDER BY r.created_at LIMIT ?`
	args = append(args, limit)
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InsightStep
	for rows.Next() {
		var s InsightStep
		if err := rows.Scan(&s.Project, &s.Step, &s.Status, &s.Attempt, &s.QueuedAt, &s.StartedAt, &s.FinishedAt, &s.RunCreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// InsightTests is one test summary of a step attempt.
type InsightTests struct {
	Project                               string
	At                                    int64
	Total, Passed, Failed, Skipped, Flaky int64
}

// InsightTestSummaries returns the test summaries reported in [from, to), for one Flight and one branch (each "" for any).
func InsightTestSummaries(q Queryer, project, branch string, from, to int64, limit int) ([]InsightTests, error) {
	query := `SELECT t.project, t.at, t.total, t.passed, t.failed + t.errored, t.skipped, t.flaky FROM test_reports t`
	args := []any{}
	if branch != "" {
		query += ` JOIN runs r ON r.id = t.run_id AND r.branch = ?`
		args = append(args, branch)
	}
	query += ` WHERE t.at >= ? AND t.at < ?`
	args = append(args, from, to)
	if project != "" {
		query += ` AND t.project = ?`
		args = append(args, project)
	}
	query += ` ORDER BY t.at LIMIT ?`
	args = append(args, limit)
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InsightTests
	for rows.Next() {
		var t InsightTests
		if err := rows.Scan(&t.Project, &t.At, &t.Total, &t.Passed, &t.Failed, &t.Skipped, &t.Flaky); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
