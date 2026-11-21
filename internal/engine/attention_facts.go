package engine

import (
	"strconv"
	"strings"
	"time"
)

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
	allowed := map[string]bool{}
	for _, p := range only {
		allowed[p] = true
	}
	runs, err := e.attentionRuns(since, only != nil, allowed)
	if err != nil {
		return nil, nil, err
	}
	deps, err := e.attentionDeployments(since, only != nil, allowed)
	if err != nil {
		return nil, nil, err
	}
	return runs, deps, nil
}

func (e *Engine) attentionRuns(since int64, filter bool, allowed map[string]bool) ([]AttentionRun, error) {
	cols := `id, project, number, status, event, ref, branch, actor, message, created_at, started_at, finished_at`
	if e.knows(schemaReruns) {
		cols += `, rerun_of`
	}
	rows, err := e.db.Query(`SELECT `+cols+` FROM runs WHERE created_at >= ? ORDER BY created_at DESC, id DESC LIMIT ?`, since, AttentionRunCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []AttentionRun
	for rows.Next() {
		var r AttentionRun
		dst := []any{&r.ID, &r.Project, &r.Number, &r.Status, &r.Event, &r.Ref, &r.Branch, &r.Actor, &r.Message, &r.CreatedAt, &r.StartedAt, &r.FinishedAt}
		if e.knows(schemaReruns) {
			dst = append(dst, &r.RerunOf)
		}
		if err := rows.Scan(dst...); err != nil {
			return nil, err
		}
		if !filter || allowed[r.Project] {
			runs = append(runs, r)
		}
	}
	return runs, rows.Err()
}

func (e *Engine) attentionDeployments(since int64, filter bool, allowed map[string]bool) ([]AttentionDeployment, error) {
	if !e.knows(schemaDeploy) {
		return nil, nil
	}
	rows, err := e.db.Query(`SELECT id, run_id, run_number, project, environment, service, version, verdict, closed_at
		FROM deployments WHERE closed_at > 0 AND closed_at >= ?`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []AttentionDeployment
	for rows.Next() {
		var d AttentionDeployment
		if err := rows.Scan(&d.ID, &d.RunID, &d.RunNumber, &d.Project, &d.Environment, &d.Service, &d.Version, &d.Verdict, &d.ClosedAt); err != nil {
			return nil, err
		}
		if !filter || allowed[d.Project] {
			deps = append(deps, d)
		}
	}
	return deps, rows.Err()
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
	out, err := e.queryRuns(`SELECT `+e.runCols()+` FROM runs WHERE id IN (`+string(marks)+`) ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	if err := e.attachBriefs(out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []*RunSummary{}
	}
	return out, nil
}

// SearchRuns finds runs by number ("12" or "#12"), by the start of a commit
// (4 or more hex digits) or by branch (prefix or contains), newest first, among
// the projects given (all if nil). It is bounded by limit and by an index on
// the run's time, and never returns more than the caller may see.
func (e *Engine) SearchRuns(only []string, q string, limit int) ([]*RunSummary, error) {
	q = strings.TrimPrefix(strings.TrimSpace(q), "#")
	if q == "" || limit <= 0 {
		return []*RunSummary{}, nil
	}
	var where []string
	var args []any
	if n, err := strconv.Atoi(q); err == nil {
		where = append(where, "number = ?")
		args = append(args, n)
	}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	if len(q) >= 4 && isHex(q) {
		where = append(where, `sha LIKE ? ESCAPE '\'`)
		args = append(args, esc+"%")
	}
	if len(q) >= 3 {
		where = append(where, `branch LIKE ? ESCAPE '\'`)
		args = append(args, "%"+esc+"%")
	}
	if len(where) == 0 {
		return []*RunSummary{}, nil
	}
	cond := "(" + strings.Join(where, " OR ") + ")"
	if only != nil {
		if len(only) == 0 {
			return []*RunSummary{}, nil
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(only)), ",")
		cond += " AND project IN (" + marks + ")"
		for _, p := range only {
			args = append(args, p)
		}
	}
	since := e.now().Add(-90 * 24 * time.Hour).UnixMilli()
	args = append(args, since, limit)
	out, err := e.queryRuns(`SELECT `+e.runCols()+` FROM runs WHERE `+cond+` AND created_at >= ? ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if out == nil {
		out = []*RunSummary{}
	}
	return out, err
}

// queryRuns runs a query that selects the run columns.
func (e *Engine) queryRuns(q string, args ...any) ([]*RunSummary, error) {
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunSummary
	for rows.Next() {
		r, err := e.scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
