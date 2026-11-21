package engine

import "time"

// leaseCand is a queued step a runner may be given: the next candidates are
// found in SQL, and each is checked again against its run's state.
type leaseCand struct {
	run, step string
	attempt   int
}

// leaseCandidates finds, oldest first, queued steps of runs this runner holds
// and, unless it should leave them to others (ownOnly), of runs no live
// runner holds. The transaction that hands one out checks again.
func (e *Engine) leaseCandidates(r RunnerInfo, now time.Time, ownOnly bool, poolWhere string, poolArgs []any) ([]leaseCand, error) {
	// Queued steps of runs this runner holds and, unless it should leave
	// them to others, of runs no live runner holds. The transaction below
	// checks again, against the run's current state.
	where, args := `runs.runner = ?`, []any{r.ID}
	if !ownOnly {
		where = `(runs.runner = ? OR runs.runner = '' OR o.last_seen IS NULL OR o.last_seen < ?)`
		args = append(args, now.Add(-e.runnerTimeout).UnixMilli())
	}
	if e.knows(schemaVerify) && r.Has(FeatureAI) {
		// An ai step needs no workspace: any runner that calls models may
		// take it, whoever holds the run.
		where = `(` + where + ` OR s.kind = 'ai')`
	}
	if e.knows(schemaParity2) {
		// A step with a runner of its own goes to any runner that matches
		// its selector (the pool filter below), whoever holds the run.
		where = `(` + where + ` OR s.own_workspace = 1)`
	}
	if e.knows(schemaReruns) {
		// Runs that need a newer server are left to one.
		where += ` AND runs.min_schema <= ?`
		args = append(args, e.schema)
	}
	if e.knows(schemaDeploy) {
		// Deploy and rollback steps go to deploy runners (LeaseDeploy).
		where += ` AND s.target = ''`
	}
	if e.knows(schemaFlow) {
		// A retry that waits is not handed out before its time.
		where += ` AND s.not_before <= ?`
		args = append(args, now.UnixMilli())
	}
	where += poolWhere
	args = append(args, poolArgs...)
	rows, err := e.db.Query(`SELECT s.run_id, s.step_id, s.attempt FROM steps s
		JOIN runs ON runs.id = s.run_id
		LEFT JOIN runners o ON o.id = runs.runner
		WHERE s.status = 'queued' AND `+where+`
		ORDER BY s.queued_at, s.run_id, s.position LIMIT 20`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cands []leaseCand
	for rows.Next() {
		var c leaseCand
		if err := rows.Scan(&c.run, &c.step, &c.attempt); err != nil {
			return nil, err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cands, nil
}
