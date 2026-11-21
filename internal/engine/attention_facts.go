package engine

import "time"

// AttentionWindow is how far back the rule looks, and AttentionRunCap the most
// runs it reads: the newest ones. Both bound the cost with a very large history.
const (
	AttentionWindow = 14 * 24 * time.Hour
	AttentionRunCap = 30000
)

// AttentionFacts reads what ComputeAttention needs: the newest runs of the last
// two weeks (of the projects only, if only is not nil), and the deployments
// that ended in that time. The decisions that wait come from Approvals.
func (e *Engine) AttentionFacts(only []string) ([]AttentionRun, []AttentionDeployment, error) {
	since := e.now().Add(-AttentionWindow).UnixMilli()
	cols := `id, project, number, status, event, ref, branch, actor, message, created_at, started_at, finished_at`
	if e.knows(schemaReruns) {
		cols += `, rerun_of`
	}
	rows, err := e.db.Query(`SELECT `+cols+` FROM runs WHERE created_at >= ? ORDER BY created_at DESC, id DESC LIMIT ?`, since, AttentionRunCap)
	if err != nil {
		return nil, nil, err
	}
	allowed := map[string]bool{}
	for _, p := range only {
		allowed[p] = true
	}
	var runs []AttentionRun
	for rows.Next() {
		var r AttentionRun
		dst := []any{&r.ID, &r.Project, &r.Number, &r.Status, &r.Event, &r.Ref, &r.Branch, &r.Actor, &r.Message, &r.CreatedAt, &r.StartedAt, &r.FinishedAt}
		if e.knows(schemaReruns) {
			dst = append(dst, &r.RerunOf)
		}
		if err := rows.Scan(dst...); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if only != nil && !allowed[r.Project] {
			continue
		}
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var deps []AttentionDeployment
	if e.knows(schemaDeploy) {
		drows, err := e.db.Query(`SELECT id, run_id, run_number, project, environment, service, version, verdict, closed_at
			FROM deployments WHERE closed_at > 0 AND closed_at >= ?`, since)
		if err != nil {
			return nil, nil, err
		}
		for drows.Next() {
			var d AttentionDeployment
			if err := drows.Scan(&d.ID, &d.RunID, &d.RunNumber, &d.Project, &d.Environment, &d.Service, &d.Version, &d.Verdict, &d.ClosedAt); err != nil {
				drows.Close()
				return nil, nil, err
			}
			if only != nil && !allowed[d.Project] {
				continue
			}
			deps = append(deps, d)
		}
		drows.Close()
		if err := drows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return runs, deps, nil
}

// RunsByID returns the full summaries of the runs with these IDs (with steps).
func (e *Engine) RunsByID(ids []string) ([]*RunSummary, error) {
	if len(ids) == 0 {
		return []*RunSummary{}, nil
	}
	marks := make([]byte, 0, len(ids)*2)
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			marks = append(marks, ',')
		}
		marks = append(marks, '?')
		args[i] = id
	}
	rows, err := e.db.Query(`SELECT `+e.runCols()+` FROM runs WHERE id IN (`+string(marks)+`) ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	var out []*RunSummary
	for rows.Next() {
		r, err := e.scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := e.attachBriefs(out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*RunSummary{}
	}
	return out, nil
}
