package store

// The queries behind the metrics endpoint's gauges. They count; they return no
// name of a repository, a branch or a person, only pools and totals. Like the
// step tables they read, they run on the connection the engine hands in until
// the engine moves into the store (stage S2).

// PoolCount is a count with the age of the oldest member, by pool.
type PoolCount struct {
	Pool     string
	N        int64
	OldestMS int64 // the oldest queued_at (0 when none)
}

func poolCounts(q Queryer, query string, args ...any) ([]PoolCount, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PoolCount
	for rows.Next() {
		var p PoolCount
		if err := rows.Scan(&p.Pool, &p.N, &p.OldestMS); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// QueuedStepsByPool counts steps waiting for a Station, by the pool their run asked for ("default" when it asked for none).
func QueuedStepsByPool(q Queryer) ([]PoolCount, error) {
	return poolCounts(q, `SELECT COALESCE(NULLIF(r.runner_pool, ''), 'default'), COUNT(*), COALESCE(MIN(s.queued_at), 0)
		FROM steps s JOIN runs r ON r.id = s.run_id WHERE s.status = 'queued' GROUP BY 1`)
}

// QueuedStepsAll counts queued steps when the database knows no pools yet.
func QueuedStepsAll(q Queryer) ([]PoolCount, error) {
	return poolCounts(q, `SELECT 'default', COUNT(*), COALESCE(MIN(queued_at), 0) FROM steps WHERE status = 'queued'`)
}

// StationsByPool counts the Stations seen since the given time (Unix milliseconds), by pool.
func StationsByPool(q Queryer, sinceMS int64) ([]PoolCount, error) {
	return poolCounts(q, `SELECT COALESCE(NULLIF(pool, ''), 'default'), COUNT(*), 0 FROM runners WHERE last_seen >= ? GROUP BY 1`, sinceMS)
}

// LeasesByPool counts the step leases held, by the pool of the Station holding them.
func LeasesByPool(q Queryer) ([]PoolCount, error) {
	return poolCounts(q, `SELECT COALESCE(NULLIF(ru.pool, ''), 'default'), COUNT(*), 0
		FROM leases l LEFT JOIN runners ru ON ru.id = l.runner GROUP BY 1`)
}

// ApprovalsWaiting counts approvals waiting for a decision and the time (Unix milliseconds) the oldest began waiting.
func ApprovalsWaiting(q Queryer) (n, oldestMS int64, err error) {
	rows, err := q.Query(`SELECT COUNT(*), COALESCE(MIN(queued_at), 0) FROM steps WHERE status = 'waiting'`)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	if rows.Next() {
		if err = rows.Scan(&n, &oldestMS); err != nil {
			return 0, 0, err
		}
	}
	return n, oldestMS, rows.Err()
}

// MirrorLagMS is the longest time (milliseconds) an enabled mirror has gone since its last success; 0 when none has succeeded yet.
func MirrorLagMS(q Queryer, nowMS int64) (int64, error) {
	rows, err := q.Query(`SELECT COALESCE(MAX(? - last_ok_at), 0) FROM repo_mirrors WHERE enabled = 1 AND last_ok_at > 0`, nowMS)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var n int64
	if rows.Next() {
		if err = rows.Scan(&n); err != nil {
			return 0, err
		}
	}
	return n, rows.Err()
}
